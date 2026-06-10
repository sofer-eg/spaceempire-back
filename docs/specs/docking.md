# Спецификация: Стыковка (phase 3.2)

Порт SP `Docking` из старой StarWind (`/home/sof/projects/go/src/starwind/sql/db.sql:10689-11960`) в MVP-объёме фазы 3.2.

## Источник в старом StarWind

В SP `Docking` три операции (`operation_type`):

| operation_type | Семантика SP | Применимо к target_type |
|----------------|--------------|--------------------------|
| 1 | object «забирает в трюм/кабину» цель: target.`location` ← object.full_id | 0 (user), 5 (ship), 8 (container) |
| 2 | object «пристыковывается»: object.`location` ← target.full_id | 1*, 2 (shipyard), 3 (trade_station), 4 (station), 5 (ship-as-host), 13 (factory) |
| 3 | прыжок через ворота: object.`sector`/`pos` ← gate-side другого сектора | 1 (gate, builded) |

\* target_type=1 без builded превращается в operation_type=2; с builded — operation_type=3.

В новой версии:
- **Прыжок** уже реализован (`executeJump` в `internal/sector/handoff.go`, поведение покрывает op=3). Эта спека прыжок не трогает — упоминает для полноты.
- **Семантика op=1** (контейнеры, пассажирские посадки, board-as-passenger) — НЕ в скоупе 3.2 (отложено, нужно после контейнеров фазы 4-5).
- **Семантика op=2** для 4 типов статиков (station, shipyard, trade_station, pirbase) — это и есть **скоуп 3.2**.

## 1. Domain-модель

### Ship (расширение)

```go
type Ship struct {
    // ...
    Docked          *EntityRef // nil = в космосе; else = к чему пристыкован
    AutoPilotModule bool       // флаг наличия модуля авто-стыковки / авто-прыжка
}
```

`Docked.Kind` ∈ `{EntityKindStation, EntityKindShipyard, EntityKindTradeStation, EntityKindPirbase}`.

В дальнейших фазах допустимы `EntityKindShip` (board-as-passenger) и условный `EntityKindContainer` — пока выходят за scope.

### Course (расширение)

```go
type Course struct {
    Sector SectorID
    Pos    Vec2
    Dock   *EntityRef // когда задан, по прибытии срабатывает авто-стыковка (при наличии модуля)
}
```

`Dock` указывает на статик, к которому игрок хочет пристыковаться. `Course.Pos` ставится равной `target.ObjectPos()`. Если `Dock == nil` — обычный курс «до координаты», авто-стыковка не срабатывает.

### EntityRef и DockableObject

Используются уже определённые `EntityRef` и `DockableObject` (`internal/domain/dockable.go`).

## 2. Операции

### Dock(ship, target) → mutates ship

Предусловия (любое нарушение возвращает ошибку, ship не изменяется):

1. `ship.Docked == nil` (иначе `ErrAlreadyDocked`).
2. `target.ObjectSector() == ship.SectorID` (иначе `ErrTargetSectorMismatch`).
3. `ship.Pos.Sub(target.ObjectPos()).Length() <= DockRange` (иначе `ErrDockOutOfRange`).

Постусловия:

- `ship.Docked = &target.ObjectID()` (копия).
- `ship.Pos = target.ObjectPos()` (корабль «приклеивается» к станции, как в SP — `shp.location=target_id_full` + nullификация скоростей).
- `ship.Vel = Vec2{}`.
- `ship.Target = nil`.
- `ship.FinalTarget = nil` (после стыковки автопилот сбрасывается; повторный курс — отдельная команда после undock).

### Undock(ship) → mutates ship

Предусловия:

1. `ship.Docked != nil` (иначе `ErrNotDocked`).

Постусловия:

- `ship.Docked = nil`.
- `ship.Vel = Vec2{}` (корабль вылетает «с нуля»).
- `ship.Target = nil`, `ship.FinalTarget = nil`.

Позиция (`ship.Pos`) **не** изменяется — корабль остаётся в точке статика. Этого достаточно для MVP; небольшое смещение «по направлению наружу станции» добавим, если коллизии станут проблемой.

## 3. Авто-стыковка (tick-driven)

Аналог `tryAutoJump` для статиков:

В каждом тике после `applyMovement`:

```
for ship in sector.ships:
    if ship.Docked != nil:                                          # уже пристыкован
        continue
    if !ship.AutoPilotModule:                                       # нет модуля — не стыкуется автоматически
        continue
    if ship.FinalTarget == nil || ship.FinalTarget.Dock == nil:     # нет курса со стыковкой
        continue
    if ship.FinalTarget.Sector != ship.SectorID:                    # ещё лететь через ворота
        continue
    target := lookupStatic(sector.statics, *ship.FinalTarget.Dock)
    if target == nil:                                               # цель исчезла (битая, разрушена и т.п.)
        ship.FinalTarget = nil
        markDirty(ship.ID)
        continue
    if !inRange(ship.Pos, target.ObjectPos(), DockRange):
        continue
    Dock(ship, target)                                              # успех — immediate persist
    markDirty(ship.ID)
```

`lookupStatic` пробегает `sector.statics` по `EntityRef.Kind` и `ID`.

`DockRange` живёт в `sector.Config` рядом с `GateRange` (значение по умолчанию — синхронно с GateRange, 30).

## 4. Ручная стыковка/расстыковка

Команды через inbox (как `SetCourseCommand`):

- `DockCommand{PlayerID, ShipID, Target EntityRef, Reply}` — применяет `Dock(...)` с теми же предусловиями, что и авто-вариант (включая проверку модуля? **нет** — ручная стыковка модуля не требует, см. ответ пользователя на вопрос «без модуля корабль ждёт ручной dock»).
- `UndockCommand{PlayerID, ShipID, Reply}` — применяет `Undock(...)`.

Ownership: `ship.PlayerID == c.PlayerID`, иначе `ErrForbidden`.

## 5. Persistence

- **Immediate.** При успешной стыковке/расстыковке вызывается `repo.Save(ctx, ship)` синхронно (как в `executeJump`). Стыковка — критическое событие, не должно теряться при крэше.
- **Поле `Docked`** в БД хранится как пара колонок `docked_kind SMALLINT, docked_id BIGINT` (`NULL` ⇔ в космосе). CHECK-constraint `docked_kind IS NULL` ↔ `docked_id IS NULL`.
- **Поле `AutoPilotModule`** — `BOOLEAN NOT NULL DEFAULT TRUE`. Default true чтобы не сломать существующие seed-кораблей до того, как появится механика оснащения (фаза 3.6+).
- **Поле `Course.Dock`** — две колонки `final_target_dock_kind SMALLINT, final_target_dock_id BIGINT`. CHECK: пара NULL/NOT-NULL; если NOT NULL, то и `final_target_sector` NOT NULL (Dock ⇒ Course).

Миграция: `0005_docking.sql`.

## 6. API

### POST /api/cmd/dock

Request:
```json
{"shipID": 1, "target": {"kind": 2, "id": 5}}
```

Response:
- 200 `{}` при успехе.
- 400 `{"error": "out_of_range"|"target_not_in_sector"|"target_not_found"|"already_docked"}`.
- 401 без сессии.
- 403 `ship.PlayerID != session.PlayerID`.

### POST /api/cmd/undock

Request:
```json
{"shipID": 1}
```

Response:
- 200 `{}`.
- 400 `{"error": "not_docked"}`.
- 401 / 403 / 404 по аналогии.

`set-course` остаётся прежним, но в DTO добавляется опциональное поле `dock {kind,id}`, которое прокидывается в `Course.Dock` (без него — обычный курс).

## 7. Frontend

- Снапшот корабля в WS получает поле `docked: {kind: number, id: number} | null`.
- Когда у текущего корабля игрока `docked != null`: правый сайдбар показывает заглушку
  «Пристыкован к {Station|Shipyard|TradeStation|Pirbase} #{id}» + кнопку «Отстыковаться»
  (POST `/api/cmd/undock`). Полный UI станции — задача 3.8.
- Канвас в этом состоянии не реагирует на ЛКМ (нельзя задавать курс, пока пристыкован).

## 8. Edge cases / acceptance

| Сценарий | Ожидание |
|----------|----------|
| Dock к статику в другом секторе | `ErrTargetSectorMismatch`, ship без изменений |
| Dock когда уже пристыкован | `ErrAlreadyDocked` |
| Dock когда далеко | `ErrDockOutOfRange` |
| Dock к несуществующему target | `ErrTargetNotFound` (статик не найден в sector.statics) |
| Undock когда не пристыкован | `ErrNotDocked` |
| Авто-стыковка без модуля | Корабль приходит в Pos и стоит; `Course` остаётся, ничего не происходит, пока игрок не вызовет `/dock` |
| Авто-стыковка к разрушенному за время полёта статику | `Course.Dock` сбрасывается (`FinalTarget = nil`) |
| Прыжок через ворота с `Course.Dock` | `executeJump` сохраняет `FinalTarget` (включая `Dock`) — авто-стыковка сработает в конечном секторе |
| Set-course во время стыковки | команда игнорируется (или ошибка); состояние Docked фиксирует корабль |

## 9. Что НЕ делается в 3.2 (отложено)

- Op=1 (контейнер pickup, посадка пассажиром на корабль) — нужно после реализации контейнеров (фазы 4-5).
- Hanger small/capital, dock_module-апгрейды, hidden-ships — нужно после системы апгрейдов (фаза 3.6+).
- `attack_type`, `in_fleet`, `parrots_to_achieve`, `speed_compare`, ориентация по направлению — фронт-боёвка (фаза 4) и AI (фаза 5).
- `messages_sys` (системные сообщения после стыковки) — фаза 6.
- Тосты «Стыковка завершена», «Аренда требуется» — после реализации соответствующих систем.
- Полноценный UI станции (торговля, ангары, лаунчер, информация) — задачи 3.3-3.8.

## Ссылки

- Старая SP: `/home/sof/projects/go/src/starwind/sql/db.sql:10689-11960`.
- CLAUDE.md старого проекта: раздел «Автостыковка (SP Docking)».
- Дизайн: фаза 3, секция 5 (методика портирования SP).
- Сопряжённые модули в новой версии: `internal/sector/handoff.go` (`executeJump`), `internal/sector/autopilot.go`, `internal/sector/autojump.go`.
