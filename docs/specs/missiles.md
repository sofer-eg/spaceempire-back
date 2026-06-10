# Спецификация: Ракеты (phase 4.3)

Самонаводящиеся снаряды: игрок запускает ракету в выбранную цель;
ракета летит к цели с проактивным наведением, взрывается при попадании
(damage в HP/Shield цели) или испаряется по TTL. Порт SP `TO_Missiles`
(`starwind/sql/db.sql:28942`).

Полная авторская версия SP делает много специфичных вещей — random hit
roll, ship-size + pro-level modifier, cross-sector hop через ворота. В
4.3 портируем именно **физику самонаведения** (direction unit-vector,
acceleration vector, strafe-compensation, friction, rotation matrix),
без random-roll и cross-sector hop — эти подкатегории отложены.

## 1. Domain-модель

### `domain/missile.go`

```go
type MissileID int64

type Missile struct {
    ID          MissileID
    SectorID    SectorID
    OwnerShipID ShipID       // attacker — для логов/AOI и killCredit в 4.6
    PlayerID    PlayerID     // owner — для AOI фильтрации владения

    Pos         Vec2
    Vel         Vec2         // signed per-tick velocity vector
    Direction   Vec2         // unit nose vector (SP `direction_x/y`)

    Target      EntityRef    // последняя зафиксированная цель
    LastTargetPos Vec2       // последняя видимая позиция цели — нужна
                             // если target вышла из сектора/умерла

    Damage      int          // ущерб при попадании (фиксирован в Launch)
    Speed       float64      // max |Vel|
    Accel       float64      // линейное ускорение по direction
    TurnRate    float64      // mis_rad_speed (rad/tick) — макс. угловая
    HitRadius   float64      // dist <= hit_radius → попадание

    ExpiresAt   time.Time    // конец TTL — после удалить
}
```

**НЕ персистится.** Ракеты — reconstructable state (дизайн 1.3 / задача
4.3). При рестарте сервера все ракеты исчезают. Хранятся только в RAM
сектора (`sectorState.missiles`).

**Параметры (Damage/Speed/Accel/TurnRate/HitRadius/TTL)** в 4.3 берутся
из единого `combat.MissileSpec` (один класс ракет). Будущие фазы могут
ввести каталог классов; до этого hard-coded дефолты в combat-пакете.

## 2. Goods integration

Стрельба расходует одну единицу goods_type `id = 50` (`Missile`, space=2)
из cargo стреляющего корабля.

### Миграция `0017_missile_goods.sql`

```sql
INSERT INTO goods_types (id, name, space) VALUES (50, 'Missile', 2);
```

Удалить в `Down`.

### Spawner

Новый игрок получает 5 ракет при создании старта. В
`shipSpawner.SpawnFor` после `repo.Create(ship)` — `cargoRepo.Add(
shipRef, MissileGoodsType, 5)`. Spawner получает `cargo.Repo` через
конструктор.

## 3. combat-пакет

### `combat/missile.go`

```go
// MissileSpec — параметры одного класса ракет. На 4.3 единственный класс.
type MissileSpec struct {
    Damage    int           // damage on hit
    Speed     float64       // максимальная |Vel|
    Accel     float64       // линейное ускорение (units/tick² когда dt=1)
    TurnRate  float64       // макс. угловая скорость (rad/tick когда dt=1)
    HitRadius float64       // радиус попадания (≤ → MissileHit)
    TTL       time.Duration // время жизни до expire
    StrafeK   float64       // SP `mis_strafe = 0.8*acceleration`
    FrictionK float64       // SP `mis_rub = -0.1*|speed|` → friction coef
}

// DefaultMissileSpec — дефолт для 4.3. Один класс, hard-coded.
func DefaultMissileSpec() MissileSpec

// LaunchMissile создаёт ракету в точке корабля с его направлением;
// начальная Vel = attacker.Vel (как в SP — наследует импульс).
func LaunchMissile(
    id domain.MissileID,
    spec MissileSpec,
    attacker *domain.Ship,
    target domain.EntityRef,
    targetPos domain.Vec2,
    now time.Time,
) *domain.Missile

// MissileOutcome — что должен сделать sector с ракетой после Tick.
type MissileOutcome uint8
const (
    MissileKeep   MissileOutcome = iota // продолжает полёт
    MissileHit                           // попал в цель (TargetAlive!)
    MissileExpired                       // TTL вышло — удалить
)

// TickMissile интегрирует одну ракету за `dt`. targetAlive=true
// если цель ещё в этом секторе и жива — тогда TickMissile получает
// её актуальную позицию; иначе использует m.LastTargetPos.
//
// Возвращает MissileHit, когда расстояние до целевой точки ≤
// spec.HitRadius (и targetAlive — мёртвую/ушедшую не "поражает").
// MissileExpired — when now >= m.ExpiresAt. Иначе MissileKeep.
func TickMissile(
    m *domain.Missile,
    targetPos domain.Vec2,
    targetAlive bool,
    spec MissileSpec,
    dt float64,
    now time.Time,
) MissileOutcome
```

**Алгоритм TickMissile (порт SP):**

1. Если `now >= ExpiresAt` → возвращаем `MissileExpired` без движения.
2. `delta = targetPos - m.Pos`; `range_eq = |delta|`.
3. Если `range_eq > 1`: `targetDir = delta/range_eq`. Иначе
   `targetDir = m.Direction; noTurn = true`.
4. `speed_eq = |m.Vel|`; `speedDir = m.Vel/speed_eq` (или Direction если
   eq<1).
5. **Rotation** (SP turning logic):
   - Если `noTurn || TurnRate*dt >= π` → `newDir = targetDir`.
   - Иначе: dot=targetDir·Direction, cross=targetDir⊥Direction
     (в системе координат Direction). Если |cross|<0.01 → выровнен.
     Поворачиваем Direction на ±`TurnRate*dt` в нужную сторону через
     2×2 ротацию.
6. **Acceleration**: `accel = Accel*dt`; если turning И `newDir·targetDir < 0`
   → `accel = friction*0.1 + Accel*0.1` (как в SP).
   `acc = newDir * accel`.
7. **Strafe compensation** (SP add_strafe): проекция текущей скорости
   на `targetDir` ортогональ. Компенсируем до `StrafeK*Accel*dt`.
8. **Friction** (SP rub): `rub = -FrictionK*speed_eq*dt * speedDir`.
9. `newVel = m.Vel + acc + rub + strafe`. Clamp на `Speed`.
10. `newPos = m.Pos + newVel * dt`.
11. **Hit check**: после интеграции, если targetAlive И
    `|newPos - targetPos| ≤ HitRadius` → `MissileHit`.
    Применение damage делает sector (через `target.TakeDamage` или
    обработку других kind'ов в 4.6).
12. Записать обратно `m.Pos = newPos, m.Vel = newVel, m.Direction = newDir`.

**Lost target (target ушла/умерла):** sector передаёт `targetAlive=false`,
`targetPos = m.LastTargetPos`. Hit-check не срабатывает (если она
вернётся в зону — игнорируем; SP в этом сценарии тоже не попадает,
только expire по ttl).

**Замечание по `speed_k` из SP.** В SP `pos = pos + speed*speed_k`
(speed_k=4.5). У нас `dt` в `tickSector` уже в секундах (cfg.TickInterval),
поэтому `speed_k` отображается в `DefaultMissileSpec.Speed/Accel`
(калибрация под текущий tick rate).

## 4. Sector integration

### `sectorState`

```go
type sectorState struct {
    // ... уже есть
    missiles      map[domain.MissileID]*domain.Missile
    nextMissileID domain.MissileID  // монотонный счётчик в пределах worker'а
    missileImpacts []MissileImpact   // per-tick events (взрывы)
}

type MissileImpact struct {
    MissileID      domain.MissileID
    AttackerShipID domain.ShipID
    Target         domain.EntityRef
    Pos            domain.Vec2
    Damage         int
    Killed         bool  // цель погибла этим попаданием
    Expired        bool  // true = expired (без damage), false = реальный hit
}
```

`nextMissileID` — простой счётчик, сбрасывается при рестарте worker.
Достаточно для in-memory only state.

### `missiles.go`

```go
// tickMissiles интегрирует все ракеты сектора за dt и формирует
// missileImpacts. После tickMissiles вызывается impactToHits, который
// для kind=ship применяет TakeDamage.
func tickMissiles(s *sectorState, dt float64, now time.Time)
```

Логика:
1. Для каждой ракеты:
   - Найти `targetAlive`/`targetPos`:
     - Если `Target.Kind=EntityKindShip`: lookup `s.ships[ShipID]`.
       Жив и в этом секторе → alive=true, pos=ship.Pos, обновить
       LastTargetPos.
     - Иначе → alive=false, pos=m.LastTargetPos.
   - `TickMissile(m, pos, alive, spec, dt, now)`.
   - `MissileExpired` → удалить из карты, push MissileImpact{Expired:true}.
   - `MissileHit` → damage в цель (kind=Ship), MissileImpact{Killed=...},
     remove. markDirty target.
   - `MissileKeep` → ничего.

### `tickSector` (worker.go)

После `fireLasers(s)`:

```go
tickMissiles(s, dt, w.clock.Now())
```

В конце тика, после `publishSnapshotFor`:

```go
s.clearMissileImpacts()  // missileImpacts живут один тик
```

### `LaunchMissileCommand`

```go
type LaunchMissileCommand struct {
    PlayerID  domain.PlayerID
    ShipID    domain.ShipID
    Target    domain.EntityRef
    Reply     chan<- LaunchMissileResult
}

type LaunchMissileResult struct {
    Err       error
    MissileID domain.MissileID
}
```

В apply:
- Ship не найден → `ErrShipNotFound`.
- Ownership mismatch → `ErrForbidden`.
- Ship.Docked != nil → `ErrShipDocked` (нельзя стрелять из дока).
- Target.Kind != EntityKindShip → `ErrInvalidAttackTarget` (4.3 поддерживает
  только ship-targets — для других kind'ов damage routing появится в 4.6).
- Target.ID == ShipID → `ErrInvalidAttackTarget`.
- Цель не в нашем секторе/удалена — `ErrInvalidAttackTarget` (на момент
  Launch цель должна быть в том же секторе).
- OK → создать missile через `combat.LaunchMissile`, инкрементить
  `nextMissileID`, добавить в map. Reply с `MissileID`.

Cargo-расход выполняется на HTTP-handler уровне до Send (см. §5);
sector сам cargo не трогает.

### Snapshot / Patch

```go
type Snapshot struct {
    // ... уже есть
    Missiles []domain.Missile  // живые ракеты этого тика
    MissileImpacts []MissileImpact
}

type Patch struct {
    // ... уже есть
    MissilesAdded   []domain.Missile
    MissilesUpdated []domain.Missile
    MissilesRemoved []domain.MissileID
    MissileImpacts  []MissileImpact
}
```

`buildPatch` для ракет: тот же diff-pattern, что для ships (`Added` =
ракета, которой не было в prev; `Updated` = была и Pos/Vel изменились;
`Removed` = была в prev, нет в curr).

AOI-фильтрация ракет: точка `m.Pos` в радиусе sub.Radius от sub.Center
→ ракета видима. `MissileImpacts`: точка `Pos` в AOI window.

## 5. cargo расход (HTTP-handler уровень)

Транзакционность между cargo (DB) и sector (RAM) обеспечивается на
уровне HTTP-handler:

1. handler принимает запрос.
2. `cargo.Service.Consume(ctx, ship, missileGoodsType, 1)` — atomic
   subtract в Postgres tx. Если ErrInsufficientQuantity → 400.
3. `sector.Send(LaunchMissileCommand)` + ждать reply.
4. Если sector ответил ошибкой — `cargo.Service.Refund(ctx, ship,
   missileGoodsType, 1)` (= Add). Логировать refund failures.
5. OK → 200 с MissileID.

### Новые методы cargo.Service

```go
func (s *Service) Consume(ctx context.Context, owner EntityRef, gtype GoodsTypeID, qty int64) error
// внутри tx: проверить наличие, Subtract.
// ErrInsufficientQuantity если нет.

func (s *Service) Refund(ctx context.Context, owner EntityRef, gtype GoodsTypeID, qty int64) error
// = tx.Add (без capacity check — refund не превышает то, что было).
```

## 6. HTTP

### `POST /api/cmd/launch-missile`

Request:
```json
{ "shipID": 17, "targetRef": { "kind": 1, "id": 23 } }
```

Response (200):
```json
{ "ok": true, "missileID": 42 }
```

Ошибки:
- `400` invalid json / invalid target (non-ship / self) / no missile in cargo
- `403` ship belongs to another player
- `404` ship not found / target not found
- `503` sector busy / cargo service unavailable
- `504` command timeout
- `500` other

### Аутентификация

Через `s.protect(...)` — стандарт, как у `attack`.

## 7. WebSocket DTO

`dto.Snapshot` пополняется:

```jsonc
{
  // ... как было
  "missilesAdded":   [...],
  "missilesUpdated": [...],
  "missilesRemoved": [42, 43],
  "missileImpacts":  [
    {
      "missileID": 42,
      "attacker": 17,
      "target": { "kind": 1, "id": 23 },
      "x": 105, "y": 200,
      "damage": 30,
      "killed": false,
      "expired": false
    }
  ]
}
```

DTO для Missile:

```jsonc
{
  "id": 42,
  "attacker": 17,
  "target": { "kind": 1, "id": 23 },
  "x": 105, "y": 200,
  "vx": 12, "vy": 5,
  "dirX": 0.92, "dirY": 0.39,
  "expiresAt": "2026-05-28T11:00:00Z"
}
```

## 8. Frontend

Минимальный обвес, как у лазеров в 4.2:

- `api.ts`: тип `Missile`, `MissileImpact`, метод `sendLaunchMissile`.
- `SectorCanvas`: render слой missiles (точка + короткий хвост по
  direction); render слой missileImpacts (короткая вспышка взрыва,
  один кадр).
- `ObjectActionsMenu`: пункт «Запустить ракету» виден когда выбранная
  цель — корабль (kind=1) и **не наш**. Цвет акцентный (магента),
  отличается от «Атаковать (лазер)».
- `useWorldState`: накатывать missilesAdded/Updated/Removed в локальный
  state; missileImpacts кадрить один tick.

## 9. Тесты

### Unit (`combat/missile_test.go`)

| Тест | Setup | Ожидание |
|---|---|---|
| LaunchMissile_InheritsAttackerVel | attacker Vel=(5,0) | missile.Vel=(5,0) |
| LaunchMissile_ZeroDirection_DefaultsToNoseToTarget | direction=(1,0) target right | direction sane |
| TickMissile_StraightHit | head-on stationary target, distance > Speed*dt | через N тиков MissileHit |
| TickMissile_Expired | now > ExpiresAt | MissileExpired |
| TickMissile_NoHit_TargetMoved | targetAlive=true, target вне HitRadius | MissileKeep |
| TickMissile_TargetLost_KeepFlying | targetAlive=false | летит к LastTargetPos, не Hit |
| TickMissile_Turning | target слева 90°; TurnRate small | direction поворачивается |
| TickMissile_AllowsInstantTurnIfFastEnough | TurnRate*dt > π | direction = targetDir сразу |

### Sector tick (`sector/missiles_test.go`)

| Тест | Setup | Ожидание |
|---|---|---|
| Tick_MissileHitsShip | A пускает в B на расстоянии=300, Speed=100/tick | через 3-4 тика snapshot.MissileImpacts.Killed?/Damage>0; missile removed; target.HP < MaxHP |
| Tick_MissileExpires | target очень далеко, TTL=2 тика | impact.Expired=true, missile removed |
| Tick_TargetDiedMidFlight | target умер после launch | missile продолжает летать до LastTargetPos, expire по TTL |
| LaunchCommand_OK | valid launch | reply.MissileID > 0, missiles map содержит запись |
| LaunchCommand_InvalidTarget_Station | target.kind=station | ErrInvalidAttackTarget |
| LaunchCommand_SelfTarget | target.ID == ShipID | ErrInvalidAttackTarget |
| LaunchCommand_NotOwner | playerID не владеет ship | ErrForbidden |
| LaunchCommand_Docked | ship.Docked != nil | ErrShipDocked |

### HTTP (`api/launch_missile_test.go`)

| Тест | Запрос | Ожидание |
|---|---|---|
| LaunchMissile_OK | player owns ship, есть missile в cargo, target=ship | 200, cargo Missile -=1 |
| LaunchMissile_NoCargo | в cargo нет ракет | 400 "missile not in cargo" |
| LaunchMissile_NonShipTarget | target.kind=station | 400 |
| LaunchMissile_NotOwner | player A → ship B | 403 |
| LaunchMissile_SectorRejects_RefundsCargo | sector reply Err=ErrInvalidAttackTarget | cargo Missile вернулась (= start) |

## 10. Критерии приёмки (из задачи 4.3)

- [ ] Ракеты летят, попадают, наносят damage
- [ ] При перезагрузке сервера ракеты исчезают (reconstructable)
- [ ] Frontend визуализирует полёт

## 11. Отложено

- **Атака не-ship целей** (station, gate, container). 4.6 (`KillObject`).
- **Cross-sector hop через ворота** (SP TO_Missiles делает hop, у нас
  4.3 — только intra-sector). Можно вернуть в дальнейшем как 4.x или
  отдельным таском.
- **Random hit roll + ship-size + pro-level modifier** из SP — для
  simplification в 4.3 ракета попадает детерминированно если в
  HitRadius. Pro-level апгрейды появятся в фазе 5.
- **Несколько классов ракет** (`ct_missiles`). Один тип в 4.3.
- **Самоуничтожение при `TTL`** не пишет system message игроку (нет
  пока инфраструктуры messages_sys).
- **Friendly fire / hostility relations** — фаза 6.2.
