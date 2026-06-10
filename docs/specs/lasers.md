# Спецификация: Лазеры (phase 4.2)

Прямая боевая петля: корабль выбирает другой корабль целью, тратит
энергию каждый тик и наносит урон до тех пор, пока цель не умерла, не
вышла за радиус, не покинула сектор или атакующий не дал `cease-fire`.

Логика стрельбы лучевым оружием в старой StarWind размазана по нескольким
SP (`TO_*`); отдельной «laser tick» SP нет. В новой версии собираем её
явно в `combat/laser.go` и вызываем из `sector.tickSector` после
`chargeShields`.

## 1. Domain-модель

### Ship (расширение)

```go
type Ship struct {
    // ... уже есть
    HP, MaxHP                  int      // 4.1
    Shield, MaxShield          int      // 4.1
    ShieldRecharge             int      // 4.1

    // Энергия — отдельный пул (как HP/Shield). Per-tick recharge.
    // MaxEnergy==0 = ship без энергоустановки (не стреляет, не
    // активирует апгрейды).
    Energy, MaxEnergy          int
    EnergyRecharge             int

    // Лазер: фиксированные параметры на корабле. На будущей фазе
    // балансного каталога классы будут задавать другие значения; до
    // этого все корабли стартуют с дефолтами spawner'а.
    // LaserDamage==0 = у корабля нет лазера, FireLaser сразу no-op.
    LaserDamage                int
    LaserRange                 float64  // максимальная дистанция выстрела
    LaserEnergyCost            int      // расход энергии за один tick-hit

    // AttackTarget — текущая боевая цель. nil = огонь не ведётся.
    // Сбрасывается, когда: цель умерла, цель вышла из сектора, игрок
    // прислал CeaseFireCommand, владелец сам убит. Отделено от
    // CurrentTargetRef (тот — навигационный «куда лечу»).
    AttackTarget *EntityRef
}
```

**Почему `int` для damage/energy.** Согласовано с Shield/HP. На уровне
тика — целочисленные единицы, как в SP.

**Почему параметры лазера — на корабле, а не в balance.yaml.** Каталог
ship-classes на старте отсутствует (см. 3.5 — balance.yaml пока только
goods). Karpathy #2: вводить классы и upgrade-резолвер ради одной
характеристики оружия — overkill. Когда появится класс — переедем.

### Где живёт `AttackTarget`

- В `Save` пишется immediate-write при изменении (AttackCommand /
  CeaseFireCommand / target killed). Это критическое игровое событие,
  как HP=0 — нельзя терять при рестарте сервера.
- В `BatchUpdate` (периодическом снапшоте) НЕ обновляется: ничего не
  меняется между immediate-событиями.
- `Energy` — class-fixed (как MaxSpeed); `MaxEnergy/EnergyRecharge/
  LaserDamage/LaserRange/LaserEnergyCost` — class-fixed, не пишутся в
  Save/BatchUpdate.

## 2. Чисто-доменный API: `internal/combat`

### `combat/energy.go`

```go
// ChargeEnergy bumps ship.Energy by EnergyRecharge per tick, clamped
// to MaxEnergy. Returns true on change. MaxEnergy==0 → skip.
func ChargeEnergy(ship *domain.Ship) bool
```

### `combat/laser.go`

```go
// LaserBeam describes one fired shot. From and To are world coordinates
// captured the tick of the shot — the worker uses them to push a
// one-frame visual effect to subscribers. Damage is the post-shield
// HP-equivalent (for logging/HUD); the actual hp/shield numbers are
// already inside the target.
type LaserBeam struct {
    AttackerShipID domain.ShipID
    Target         domain.EntityRef
    From, To       domain.Vec2
    DamageDealt    int    // sum of ShieldAbsorbed + HPAbsorbed
    Killed         bool   // target HP reached 0 this shot
}

// FireLaser runs one tick of laser fire from attacker against target.
//
//   ok == false, beam == LaserBeam{}: no shot this tick — attacker too
//                                     far, out of energy, or has no
//                                     laser equipped. AttackTarget is
//                                     left as-is.
//   ok == true:  beam is the resulting shot. attacker.Energy was
//                debited; target took damage. Sector worker must add
//                beam to the per-tick effect slice and clear
//                attacker.AttackTarget if beam.Killed.
//
// Range check uses attacker.Pos / target.Pos. Both must already be in
// the same sector — the caller (worker) guarantees this by only passing
// targets it found in its own ship map.
func FireLaser(attacker, target *domain.Ship) (LaserBeam, bool)
```

## 3. Sector integration

### tickSector

```go
func (w *Worker) tickSector(ctx context.Context, s *sectorState, dt float64) {
    started := w.clock.Now()
    resolveAutopilot(s, w.router, w.cfg.DockRange)
    applyMovement(s, dt)
    w.tryAutoJump(s)
    chargeShields(s)
    chargeEnergies(s)         // ← новое 4.2
    fireLasers(s)             // ← новое 4.2
    w.runProduction(ctx, s, started)
    w.persistDirty(ctx, s)
    s.tick++
    broadcastPatches(...)
    publishSnapshotFor(s, elapsed)
    s.clearEffects()          // ← effects живут один тик
}
```

### Effects в `sectorState`

```go
type sectorState struct {
    // ...
    laserEffects []domain.LaserBeam   // накопленные за текущий тик
}
```

После `publishSnapshotFor` слайс очищается — он already-snapshotted в
`Snapshot.Effects`.

### Snapshot и патч

`Snapshot.Effects []LaserBeam` — короткоживущие события текущего тика.
Patch builder включает effects всегда (не diff) — каждый тик новый
снимок. Подписчики получают только эффекты, связанные с видимыми
кораблями (AOI-фильтрация: если attacker или target — внутри AOI окна).

### AttackCommand / CeaseFireCommand

```go
type AttackCommand struct {
    ShipID domain.ShipID
    Target domain.EntityRef
    Reply  chan<- CmdResult // optional — для тестов и ack
}

type CeaseFireCommand struct {
    ShipID domain.ShipID
    Reply  chan<- CmdResult
}
```

Применяются в начале тика (drainInbox). Валидации:
- Корабль с `ShipID` существует в этом секторе, иначе `ErrShipNotFound`.
- Target.Kind == EntityKindShip (на 4.2 только корабли). Иначе
  `ErrInvalidAttackTarget`.
- AttackCommand для собственного корабля = no-op (Karpathy #2:
  «нельзя стрелять в самого себя» — обработать молча через
  `ErrInvalidAttackTarget`).
- CeaseFireCommand игнорируется, если AttackTarget уже nil.

После успешного применения — immediate `repo.Save(ctx, ship)`.

## 4. WS protocol

Расширяется patch DTO в `internal/api/dto` (или там, где живёт):

```jsonc
{
  "tick": 42,
  "ships": { ... },   // как раньше
  "effects": [
    {
      "kind": "laser",
      "attacker": 17,
      "target": { "kind": "ship", "id": 23 },
      "from": { "x": 100, "y": 200 },
      "to":   { "x": 350, "y": 220 },
      "damage": 8,
      "killed": false
    }
  ]
}
```

Если в текущем тике нет эффектов — поле `effects` опущено или пустой
массив.

## 5. Команды клиента

| HTTP | Тело | Сектор command |
|---|---|---|
| `POST /api/cmd/attack` | `{ "shipID": 17, "targetRef": {"kind":1, "id":23} }` | `AttackCommand` |
| `POST /api/cmd/cease-fire` | `{ "shipID": 17 }` | `CeaseFireCommand` |

`kind:1` = `EntityKindShip` (см. `domain.ids.go`).

Аутентификация — как у `/api/cmd/move`: handler читает сессию из
cookie, проверяет ownership ship → playerID.

## 6. Frontend

- `ObjectActionsMenu`: новый пункт «Атаковать» виден для `target.kind=ship`,
  скрыт когда target — наш собственный корабль. Когда уже атакуем эту
  цель — пункт меняется на «Прекратить огонь».
- `SectorCanvas`: новая `drawLaserBeams(effects)` рисует короткие
  красно-оранжевые отрезки `(from→to)` по эффектам последнего патча.
  Эффекты висят один кадр (фронт хранит их только до прихода
  следующего патча).
- `HudPanel`: рядом со Shield-bar появляется Energy-bar (`Energy /
  MaxEnergy`). Цвет акцентный (cyan).
- Минимальный «attack target indicator»: оранжевая рамка вокруг ship,
  на которого мы навели лазер (вторичный highlight, отличный от
  navigation `currentTargetRef`).

## 7. Тесты

### Unit (combat)

| Тест | Setup | Ожидание |
|---|---|---|
| ChargeEnergy_BelowMax | Energy=50/Max=100/Rch=10 | 60, true |
| ChargeEnergy_AtMax | Energy=100/Max=100 | 100, false |
| ChargeEnergy_Clamps | Energy=95/Max=100/Rch=10 | 100, true |
| ChargeEnergy_NoModule | Max=0 | 0, false |
| FireLaser_OutOfRange | dist=1000, Range=500 | no shot, energy untouched |
| FireLaser_OutOfEnergy | Energy=2, Cost=5 | no shot, target untouched |
| FireLaser_NoLaserModule | LaserDamage=0 | no shot |
| FireLaser_Hit | Energy=10, Cost=5, Damage=20, target Shield=10/HP=50 | Energy=5, beam: dmg=20, target Shield=0/HP=40, ok=true, killed=false |
| FireLaser_Kill | dmg overshoots | beam.Killed=true, target HP=0 |

### Sector tick

| Тест | Setup | Ожидание |
|---|---|---|
| Tick_AttackerKillsTarget | A AttackTarget=B, distance in range; N тиков | через ожидаемое число тиков B HP=0, A.AttackTarget=nil, snap.Effects содержит финальный beam.Killed |
| Tick_TargetOutOfRange_NoEffects | дистанция > range | snap.Effects пустой, A.AttackTarget остаётся |
| Tick_AttackerOutOfEnergy_Recovers | Cost=5, Recharge=2 — должен стрелять через тик после регена | беспорядочно: чередование пропуск/выстрел |
| Tick_AttackCommand_Validates | команда с target.kind=station | ErrInvalidAttackTarget, AttackTarget остаётся nil |

### HTTP handler

| Тест | Запрос | Ожидание |
|---|---|---|
| Attack_OK | session=playerA, shipID=A.ship, target=ship_of_B | 202, sector получил AttackCommand |
| Attack_NotOwner | playerA пытается атаковать с корабля playerB | 403 |
| Attack_NonShipTarget | target.kind=station | 400 |
| CeaseFire_OK | shipID=A.ship | 202 |
| CeaseFire_NotOwner | playerA → корабль playerB | 403 |

## 8. Критерии приёмки (из задачи 4.2)

- [ ] PvP: один игрок атакует другого, виден damage в WS-стриме
- [ ] Энергия тратится и регенерирует
- [ ] Frontend показывает атаку (луч) и снимает цель при гибели

## 9. Отложено

- Атака станций/дронов — атака целей не-ship kind вне scope 4.2
- Hostility/relations (friendly fire gating) — фаза 6.2
- Energy upgrades (`updates.up_energy`) — нет таблицы updates
- Damage type / armor / resistance — нет в SP оригинале на этой ступени
- Lock-on / fire-arc — не реализовано в lasers SP старой версии
