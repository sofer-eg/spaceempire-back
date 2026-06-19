# Спецификация: стыковка корабль-к-кораблю и внешняя стыковка (up_exdocking)

Порт ветки SP `Docking` (op=1/op=2 target_type=5) и пары `ExternalDockInitiate` /
`ExternalDockCheck` из старой StarWind. Делится на две задачи:

- **TASK-100.3.24 (foundation, этот документ §1–§5)** — ship-to-ship docking к
  подвижному кораблю-носителю: `Docked.Kind = EntityKindShip`, ride-along,
  занятие ангара по вместимости. Реализуется сейчас.
- **TASK-100.3.23 (up_exdocking, §6)** — мультитиковая внешняя стыковка с
  гейтом модуля `up_exdocking`, hostility-сообщениями и правилом класса 7.
  Строится поверх foundation. Документируется здесь для будущей реализации.

## Источник в старом StarWind

| Кусок SP | Что делает | Файл |
|----------|-----------|------|
| `Docking` op=2 target_type=5 | корабль садится в ангар носителя: `object.location = target_full_id` | `sql/db.sql:11138-11420` |
| `Docking` op=1 target_type=5 | носитель берёт чужой корабль в свой ангар (зеркало op=2) | `sql/db.sql:11260-11309` |
| `TO_ShipMovement` exdock-тик | инкремент `up_status` счётчика → `docked`; сброс при `fly_mode=0` | `sql/db.sql:32594-32634` |
| `ExternalDockInitiate` | гейты модуль+класс-7+relation+range → `up_status='docked,<t>'` | `sql/db.sql:12456-12707` |
| `ExternalDockCheck` | читает `up_status` → op_status 2=none/3=in-progress/4=docked | `sql/db.sql:12372-12440` |

Ключевые `restriction_type` оригинала: 2 — у носителя нет ангара нужного типа;
3/7 — ангар полон; 6 — нет ангара (ветка op=2). Эти коды маппятся в Go-sentinel'ы
(`ErrNoHangar`, `ErrHangarFull`).

---

# Часть 1 — Foundation (TASK-100.3.24)

## 1. Domain-модель

`Ship.Docked *EntityRef` уже существует (статик-докинг 3.2). Расширяется: теперь
`Docked.Kind` может быть `EntityKindShip` — корабль пристыкован к другому
(подвижному) кораблю-носителю. Сериализация/persistence уже generic
(`docked_kind SMALLINT, docked_id BIGINT`, CHECK только all-or-none) — **миграция
не нужна**, `EntityKindShip = 1` укладывается в существующую колонку.

Hanger-данные (`HangerCapital/HangerSmall/HangerShipType/HangerShipSpace`) живут в
`balance.ShipClass` и **не** копируются на `Ship`. Воркер резолвит их по
`ship.ShipClassID` через инъекцию (как `Relations`):

```go
// в sector:
type HangerStats interface {
    HangerOf(classID domain.ShipClassID) domain.Hanger
}
// domain.Hanger — projection полей balance.ShipClass:
type Hanger struct {
    Capital   int // вместимость ангара носителя для capital-кораблей
    Small     int // вместимость ангара носителя для small-кораблей
    ShipType  int // слот, который занимает корабль (1 capital, 2 small, 0 = не помещается)
    ShipSpace int // место, занимаемое кораблём в ангаре носителя
}
```

Инъекция: `WithHangerStats(h HangerStats)`. Nil → ship-to-ship dock к ангару
невозможен (вместимость трактуется как «нет ангара») — для чистых unit-тестов без
каталога. App-адаптер над `balance.ShipClasses`.

## 2. Резолв цели

`lookupShip(s *sectorState, ref EntityRef) (*domain.Ship, error)` — ищет корабль
по `ref.ID` в `s.ships` (тот же сектор). Не найден → `ErrTargetNotFound`. Ствол
для статиков (`lookupStatic`) не трогаем — ship это отдельный путь.

## 3. executeDockToShip (мутация)

Предусловия (любое нарушение → ошибка, ship не меняется):

| Условие | Ошибка | Оригинал |
|---------|--------|----------|
| `ship.Docked == nil` | `ErrAlreadyDocked` | — |
| `host.ID != ship.ID` | `ErrInvalidDockTarget` | нельзя стыковаться к себе |
| `host.SectorID == ship.SectorID` | `ErrTargetSectorMismatch` | `map.object_sector = shp.object_sector` |
| `dist(ship, host) <= DockRange` | `ErrDockOutOfRange` | `range <= DockRange` |
| `host.IsOpen \|\| host.PlayerID == ship.PlayerID` | `ErrDockNotOpen` | `object_opened = shp.opened OR same owner` |
| `!relations.IsHostile(PlayerRef(ship), PlayerRef(host))` | `ErrDockHostile` | relation-WHERE (warrate/users/race) |
| вместимость ангара (см. §4) | `ErrNoHangar` / `ErrHangarFull` | restriction 6 / 7 |

Релейшн-гейт через `w.relations` (nil → пропускаем, как остальной sector-код).
Ключи — `domain.PlayerRef(ship.PlayerID)` / `PlayerRef(host.PlayerID)` (как в
`drones.go`, `combat.go`). Same owner → `IsHostile` всё равно false, порядок
проверок не важен.

Постусловия (ride-along привязка):

- `ship.Docked = &EntityRef{Kind: EntityKindShip, ID: host.ID}`.
- `ship.Pos = host.Pos`; `ship.Vel = {}`.
- `ship.Target = nil`, `ship.FinalTarget = nil`, `ship.CurrentTargetRef = nil`,
  `ship.MiningTarget = nil` (тот же сброс, что у статик-`executeDock`).
- Immediate `repo.Save` + `markDirty` (стыковка — критическое событие; на ошибке
  Save откат `*ship = prev`).

Ручная стыковка корабля-к-кораблю **не требует модуля** (как и статик-докинг;
модуль `up_exdocking` — это слой §6, авто-внешний-докинг). Ownership на команде:
`ship.PlayerID == cmd.PlayerID`.

## 4. Вместимость ангара (порт op=2 target_type=5)

```
sh := hangers.HangerOf(ship.ShipClassID)   // footprint пристыковывающегося
ho := hangers.HangerOf(host.ShipClassID)    // вместимость носителя
slot := sh.ShipType                          // 1 capital | 2 small | 0 none
if hangers == nil || slot == 0:    ErrNoHangar          // корабль не помещается в ангар вообще
capacity := ho.Capital if slot==1 else ho.Small
if capacity == 0:                  ErrNoHangar          // у носителя нет ангара этого типа (restriction 6)
used := Σ other.HangerShipSpace for other in s.ships
        where other.Docked == ship-ref(host) && HangerOf(other).ShipType == slot
if capacity - (sh.ShipSpace + used) < 0:   ErrHangarFull  // restriction 7
```

Точный аналог SP: `target_hanger_X - (object_hanger_ship_space + used_hanger_X) < 0`.

## 5. Ride-along (carry на каждом тике)

`applyMovement` уже пропускает `Docked != nil` — пристыкованный сам не двигается
(как `location != 0` в оригинале). Новый шаг `carryDockedShips(s)` в `tickSector`
**после `applyMovement`**:

```
for ship in s.ships where ship.Docked.Kind == EntityKindShip:
    host, ok := s.ships[ship.Docked.ID]
    if !ok:          continue          // носитель ушёл из сектора/погиб — оставляем как есть (см. ниже)
    if ship.Pos != host.Pos:
        ship.Pos = host.Pos
        markDirty(ship)
```

`Undock`/`executeUndock` уже generic (Docked=nil, vel/targets сброшены) — работает
и для ship-цели без изменений.

### Вне скоупа foundation (отдельные follow-up)
- **Перенос пристыкованных при прыжке носителя через ворота** — сейчас при уходе
  носителя из сектора carried-корабль «зависает» (Docked к отсутствующему host →
  не двигается). Игрок может вручную `undock`. Авто-перенос/авто-релиз —
  отдельная задача.
- **Авто-релиз при гибели носителя** — то же: ручной undock спасает; авто —
  follow-up.
- pilot cabin / op=1 target_type=0 (пилот в кабину) — уже частично покрыто
  EVA/passenger (10.23).

---

# Часть 2 — up_exdocking (TASK-100.3.23, реализовано)

Строится поверх §1–§5. Состояние в оригинале — строка `updates.up_status`:
`<turn_count>,<target_full_id>` (в процессе) → `docked,<target_full_id>`
(завершено) → `none` (сброс при отлёте).

## 6. Мультитиковая внешняя стыковка (реализация)

**Состояние** — поле `Ship.ExternalDock *domain.ExternalDock{ Target EntityRef;
TurnsLeft int }`, **RAM-only transient** (как `MiningTarget`): не персистится
(оригинальный счётчик живёт ~1 тик). Воркер на каждом тике **заменяет**
указатель (не мутирует `TurnsLeft` на месте), чтобы алиасящий снапшот не словил
гонку. В WS DTO не попадает (`domain.Ship` без json-тегов).

**Initiate** — `ExternalDockCommand{PlayerID, ShipID, Target, Reply}`
(`internal/sector/external_dock.go`), `POST /api/cmd/exdock`:
- гейты: ownership, `Docked == nil`, **модуль `up_exdocking`**
  (`shipEquipmentLevel >= 1` → иначе `ErrEquipmentRequired`). В каталоге
  `equipment.yaml` модуль ставится **только на класс 7**, поэтому оригинальное
  правило «процесс идёт и класс ≠ 7 → нельзя переинициировать» выполняется
  автоматически: любой носитель модуля — класс 7, переинициация просто
  перезапускает счётчик.
- `externalDockGates`: self / sector / range / **не-враждебность** (§3 без гейтов
  open/owner и вместимости — внешний док для того и существует).
- успех: `ExternalDock = {Target, TurnsLeft: cfg.ExternalDockTurns}` (default 1 =
  `dock_suspension_time`).

**Per-tick** — `tickExternalDock` (tickSector, после `carryDockedShips`):
- `Docked != nil` → процесс снимается.
- `TurnsLeft > 1` → декремент (замена указателя).
- последний тик → `completeExternalDock`: ре-валидация `externalDockGates`
  (носитель мог уйти из радиуса) и `executeExternalAttach` → `applyShipDock`
  (тот же ride-along attach, что у foundation, **но без проверки вместимости
  ангара** — внешний захват за корпус). Провал валидации/исчезновение носителя →
  тихая отмена (debug-лог).

**Отмена** — `MoveCommand` / `SetCourseCommand` сбрасывают `ExternalDock` (порт
«сброса при отлёте `fly_mode=0`»). Завершённый внешний док — это обычный
`Docked`, поэтому отстыковка/отлёт работают через тот же auto-undock foundation.

**`ExternalDockCheck` (статус 2/3/4 для UI)** — не реализован: `ExternalDock`
есть в RAM-снапшоте, но в WS DTO/фронт не проброшен (отдельный фронт-таск при
необходимости). Бэкенд-механика (AC #2/#3) закрыта.

## 7. messages_sys

В Go-порте **нет** таблицы сообщений игроку (есть bus + WS push). Системные
сообщения «Стыковка запрещена/завершена» из оригинала **не портированы**:
запрет возвращается синхронным ответом команды (`ErrDock*`), завершение — это
переход в `Docked` (виден в снапшоте). WS-тосты — отдельный фронт-таск.

## Ссылки
- SP `Docking`: `/home/sof/projects/go/src/starwind/sql/db.sql:11103-11420`.
- `ExternalDockInitiate` / `ExternalDockCheck`: `sql/db.sql:12372-12707`.
- exdock per-tick: `sql/db.sql:32594-32634`.
- Foundation статик-докинга: `back/docs/specs/docking.md`.
- Go: `internal/sector/docking.go`, `autodock.go`, `tick.go`, `worker.go`;
  `internal/domain/ship.go`; `internal/balance/shipclass.go` (Hanger*-поля).
- CLAUDE.md старого проекта: разделы «Автостыковка (SP Docking)»,
  «Race AI, f_ships и перехват NPC-кораблей».
