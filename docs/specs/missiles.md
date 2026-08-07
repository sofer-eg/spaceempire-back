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

**Параметры (Damage/Speed/Accel/TurnRate/HitRadius)** копируются в саму
ракету на запуске из `combat.MissileSpec` выбранного класса, а `TTL` --
в `ExpiresAt`. `TickMissile` читает магнитуды **из ракеты**, спека ему не
передаётся: до TASK-175 она передавалась, а sector держал один
package-level `missileSpec`, поэтому любая запущенная ракета летела по
профилю класса 1 независимо от израсходованного боеприпаса.

`Class` в `domain.Missile` **не хранится**: ракета RAM-only, класс нужен
только в момент запуска (выбор спеки + строки cargo), после старта ни
один потребитель его не спрашивает -- ни persistence (её нет), ни DTO
(иконка одна). Торпеда класс несёт, потому что персистится и
восстанавливается из БД.

## 2. Goods integration

Стрельба расходует одну единицу боеприпаса выбранного класса из cargo
стреляющего корабля. Маппинг класс -> товар -- это `ct_missiles.cargo_id`
(TASK-175, до неё был провязан только класс 1):

| class | название         | goods_type | space | ст. продают | ср. цена |
|-------|------------------|-----------|-------|-------------|----------|
| 1     | Ракета Москит    | 10        | 1     | 62          | 392      |
| 2     | Ракета Оса       | 11        | 1     | 71          | 1151     |
| 3     | Ракета Стрекоза  | 12        | 1     | 72          | 2309     |
| 4     | Ракета Шелкопряд | 13        | 2     | 72          | 5778     |
| 5     | Ракета Шершень   | 14        | 3     | 67          | 11651    |

(«ст. продают»/«ср. цена» -- замер на dev-БД, `station_goods`.) Все пять
space <= 3, т.е. влезают даже в стартовый `cargobay` 50 -- в отличие от
дрона (290). Константы -- `domain.Missile*GoodsType`, потому что на них
должны сойтись три слоя: `app` (стартовый магазин, класс 1), `api`
(расход при запуске) и `sector` (kill-drop). Список целиком --
`domain.MissileGoodsTypes()`.

### Миграция `0017_missile_goods.sql` -> `0063` (TASK-167)

Изначально 0017 завела для боеприпаса **отдельный** товар:

```sql
INSERT INTO goods_types (id, name, space) VALUES (50, 'Missile', 2);
```

Это был синтетический дубликат: в каталоге уже была ракета, и ни одна
станция товар 50 не продавала -- истратив стартовый боезапас, игрок
пополнить его не мог, а ракеты 10-14, которые продаются на 62-72 рынках,
не расходовались ничем.

Каноничный маппинг класса на товар живёт в самой старой схеме StarWind --
`ct_missiles.cargo_id`:

| class | name      | power | ttl | cargo_id |
|-------|-----------|-------|-----|----------|
| 1     | Москит    | 1000  | 5   | **10**   |
| 2     | Оса       | 2500  | 6   | 11       |
| 3     | Стрекоза  | 5000  | 6   | 12       |
| 4     | Шелкопряд | 12000 | 9   | 13       |
| 5     | Шершень   | 25000 | 10  | 14       |

`0063_consolidate_ammunition_goods.sql` переносит cargo 50 -> 10
(со слиянием количеств, см. §2.1) и удаляет товар 50. В TASK-167 был
провязан только класс 1: каталога спеков не было, `LaunchMissileRequest`
класс не несла. TASK-175 завела каталог (`missileSpecsByClass`, см. §3.1)
и параметр класса в API, так что провязаны все пять -- 11-14 больше не
«продаётся и не расходуется».

Вместе с id восстанавливается родной `space`: 2 -> 1.

### §2.1 Слияние количеств в миграции

У `cargo` есть UNIQUE `(owner_kind, owner_id, goods_type_id,
goods_owner_id)`, поэтому простой `UPDATE ... SET goods_type_id = 10`
падает на владельце, у которого товар 10 уже есть. Миграция делает
`INSERT ... SELECT ... ON CONFLICT (те же четыре колонки) DO UPDATE SET
quantity = cargo.quantity + EXCLUDED.quantity` + `DELETE` исходных строк.
Набор колонок в `ON CONFLICT` обязан совпадать с constraint'ом --
иначе Postgres падает с 42P10 на этапе планирования (TASK-151/155).

Слияние необратимо: `Down` возвращает товар 50 в каталог и старые
англоязычные имена, но разделить сложенные количества нельзя.

### §2.2 Защита от расхождения каталога

`GET /api/goods` отдаёт `configs/balance.yaml`, а сервер считает объём
по `goods_types` в Postgres. Ничто не связывало два источника -- отсюда и
взялись 50/51. `TestIntegration_BalanceCatalog_MatchesGoodsTypesTable`
(`internal/balance/catalog_sync_integration_test.go`) сверяет их по
**id + name + space**: id-only проверка пропустила бы товар, который
подписан и померян по-разному в БД и у клиента.

### Spawner

Новый игрок получает 5 ракет при создании старта. В
`shipSpawner.SpawnFor` после `repo.Create(ship)` — `cargoRepo.Add(
shipRef, MissileGoodsType, 5)`. Spawner получает `cargo.Repo` через
конструктор.

## 3. combat-пакет

### `combat/missile.go`

```go
// MissileSpec — параметры одного класса ракет (их пять, см. §3.1).
type MissileSpec struct {
    Damage    int           // damage on hit
    Speed     float64       // максимальная |Vel|
    Accel     float64       // линейное ускорение (units/tick² когда dt=1)
    TurnRate  float64       // макс. угловая скорость (rad/tick когда dt=1)
    HitRadius float64       // радиус попадания (≤ → MissileHit)
    TTL       time.Duration // время жизни до expire
}

// StrafeK/FrictionK полей нет: SP считает их из speed/acceleration
// класса фиксированными коэффициентами (0.8 и 0.1), т.е. они
// униформны -- живут как package-константы missileStrafeK /
// missileFrictionK (та же форма, что у торпед).

// DefaultMissileSpec — профиль класса 1..5; неизвестный класс (в т.ч. 0)
// откатывается на класс 1, чтобы аксессор никогда не отдал
// вырожденную спеку.
func DefaultMissileSpec(class int) MissileSpec

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
    dt float64,
    now time.Time,
) MissileOutcome
```

### §3.1 Каталог классов и калибровка (TASK-175)

`missileSpecsByClass` -- по строке на класс `ct_missiles`. Оси
калибровались **по-разному**, и это осознанно:

| class | Damage | Speed | Accel | TurnRate (rad/s) | HitRadius | TTL  |
|-------|--------|-------|-------|------------------|-----------|------|
| 1     | 1000   | 90    | 108.0 | 2.1596           | 12        | 15 s |
| 2     | 2500   | 78    | 70.2  | 1.5522           | 12        | 18 s |
| 3     | 5000   | 60    | 48.0  | 1.3497           | 12        | 18 s |
| 4     | 12000  | 28    | 21.0  | 0.9448           | 12        | 27 s |
| 5     | 25000  | 22    | 16.0  | 0.5397           | 12        | 30 s |

* **Damage = `ct_missiles.power` буквально.** Класс 1 до TASK-175 был 30 --
  реликт 4.3, когда ничего ещё не было откалибровано. Корпуса, щиты и
  лазеры перенесены из StarWind буквально (Разведчик кл. 77: hull 4040,
  shield 6900, ~146 урона лазера за выстрел; Кентавр 78: 66000 + 161000),
  так что 30 -- пятая часть ОДНОГО выстрела лазера: оружие было
  декоративным. Рыночные цены подтверждают шкалу power:
  392/1151/2309/5778/11651 кр ≈ 1 : 2.9 : 5.9 : 14.7 : 29.7 против
  1 : 2.5 : 5 : 12 : 25.
* **Speed/Accel = `ct_missiles.speed`/`.acceleration` буквально**, и это
  сдвиг единиц по решению, а не по недосмотру: SP прибавляет их раз в
  тик (т.е. они per-tick), а `MissileSpec` -- per-second; корабельный
  `MaxSpeed` при этом остался per-tick (`moveShip` делает `Pos += Vel`).
  Значит буквальное чтение делает ракету в 3 раза быстрее относительно
  корабля. Почему так: дальность ракеты = Speed × TTL, а самый широкий
  радар -- 500 units, и при per-tick чтении у класса 5 дальность была бы
  ~220 -- он не дотягивался бы ни до чего, что видит владелец, т.е. ровно
  тот дефект («продаётся и бесполезно»), который задача и закрывает.
  Лестница сохраняется при любом чтении (класс 1 в 4 раза быстрее класса
  5), и замысел оригинала «тяжёлые классы не догоняют быструю цель» тоже:
  28 и 22 u/s -- это 84 и 66 units за 3-секундный тик, ниже 130.6 (141 с
  up_engine) у топовых классов кораблей.
* **TurnRate** выводится формулой самого SP:
  `maneureability × mob_to_grad_eq (1.16)` = градусы **за тик**, делим на
  номинальный тик 3 s. Порог «мгновенного доворота» ложится там же, где в
  оригинале: SP снапит при `grad_speed > 180°/тик`, `TickMissile` -- при
  `TurnRate*dt >= π`; классы 1-3 (371/267/232 °/тик) снапят в обоих,
  классы 4-5 (162/93) доворачивают постепенно в обоих.
  **Эта эквивалентность привязана к номинальному тику**: `nominalTickSeconds`
  в `combat/missile.go` -- константа (каталог спеков намеренно не знает про
  config), а `cfg.Sector.TickInterval` штатно переопределяется. При тике 1 s
  постепенно доворачивают ВСЕ пять классов, т.е. паритет с SP по этой оси
  теряется. Допущение запинено тестом
  `TestUnit_MissileSpecs_SnapBoundaryHoldsAtTheDefaultTick`: смена дефолтного
  тика роняет его, и перекалибровка каталога становится частью такой смены.
* **TTL = `ct_missiles.ttl`, но с конвертацией**, потому что единица SP
  для него однозначно тики (`mis_ttl >= mis_std_ttl` против `ttl = ttl+1`
  раз в тик): 5/6/6/9/10 тиков × 3 s = 15/18/18/27/30 s. Класс 1
  сохраняет свои 15 s -- это была та же самая конвертация.
* **HitRadius** одинаков: в `ct_missiles` нет такой колонки (SP стартует с
  `std_hit_distance = 10` и накладывает модификаторы пилота/размера,
  которые не портированы -- см. §11), портировать по классам нечего.

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
поэтому `speed_k` растворён в калибровке Speed/Accel (см. §3.1).

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
    Class     int                  // 1..5, выбирает combat.DefaultMissileSpec
    GoodsType domain.GoodsTypeID   // 10..14, строка cargo к списанию
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
- Цель вне набора `missileTargetable` → `ErrInvalidAttackTarget`. Набор
  (`sector.IsMissileTargetKind` для не-ship-кайндов, тот же предикат гейтит
  HTTP-хэндлер — один источник истины):
  - **другой корабль** — включая **скафандр** (EVA — это строка `ships`,
    TASK-87, поэтому отдельной ветки не нужно);
  - **любая уничтожимая статика** (`IsStaticTargetKind`), **включая ворота**
    с TASK-110 — прежнее ограничение ЧТЗ C-04 снято;
  - **контейнер** (TASK-111) — единственный кайнд, где набор ракеты шире, чем у
    лазера и торпеды: те остаются на статике.
  Всё остальное (дрон, торпеда, астероид, unknown) → 400 на хэндлере.
- Target.ID == ShipID → `ErrInvalidAttackTarget`.
- Цель не в нашем секторе/удалена — `ErrInvalidAttackTarget` (на момент
  Launch цель должна быть в том же секторе).
- OK → создать missile через `combat.LaunchMissile`, инкрементить
  `nextMissileID`, добавить в map. Reply с `MissileID`.

Cargo-расход выполняется на HTTP-handler уровне до Send (см. §5);
sector сам cargo не трогает.

### Контейнер как цель (TASK-111)

Контейнер — не статика: он живёт в своей карте (`s.containers`) с TTL и своим
reap-путём, поэтому в `IsStaticTargetKind` его нет (этот набор гейтит ещё лазер и
торпеду, которым по ящикам стрелять нечего). Отдельный `IsMissileTargetKind`
добавляет только контейнер.

- **Поражаемость** — `domain.Container.TakeDamage` (щита нет), HP **только в
  RAM**: колонки нет, `Config.ContainerHP` (дефолт 25 — сильно ниже урона самой
  слабой ракеты, 1000 у класса 1 после TASK-175; было 30, вывод тот же — одно
  попадание любого класса уничтожает) штампуется на cold-start и в
  `sectorState.addContainer`, поэтому любой путь дропа (лут с килла, руда, лут
  взлома) получает корпус в одной точке. Уцелевший ящик «лечится» рестартом — так
  же, как HP прочей статики (TASK-67).
- **Гибель = отказ от лута, а не медленный подбор**: `applyMissileHitContainer`
  убирает ящик из сектора и удаляет строку (`containerRepo.Delete` — вместе с
  cargo контейнера). Ничего не выпадает: груз уничтожен. Это и есть продуктовый
  смысл — лишить противника добычи.
- **UX разведён** (AC-3): в меню объекта у контейнера обе команды рядом —
  «◈ Запустить ракету» (уничтожить с грузом, с подсказкой) и «⬚ Подобрать».
  HUD-панель вооружения контейнеры не показывает: её цель приходит из
  attack-target, а атака по-прежнему ship-only.

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

## 5. cargo расход (внутри воркера, TASK-147)

**Инвариант: боеприпас списывает воркер, в `apply`, а не HTTP-handler.**
Handler боеприпасом не владеет вообще -- он только маршрутизирует и
маппит исход (как `install-jammer`):

1. handler принимает запрос, валидирует цель (`missileTargetable`-набор) и
   класс (`missileGoodsType(class)`; вне 1..5, включая 0, -- 400
   «invalid missile class»);
2. `sector.Send(LaunchMissileCommand{..., Class: N, GoodsType: 10..14})` --
   id товара несёт команда, чтобы sector не знал каталога; класс -- чтобы
   воркер выбрал спеку;
3. ждёт ack (`AckTimeout`) и маппит: `ErrShipNotFound` → 404,
   `ErrForbidden` → 403, `ErrShipDocked` → 400, `ErrEquipmentRequired` →
   422, `ErrNotEnoughEnergy` → 422, `ErrInvalidAttackTarget` → 400,
   `cargo.ErrInsufficientQuantity` → 400 «в трюме нет ракет»,
   `cargo.ErrGoodsTypeNotFound` → 500, `ErrOrdnanceUnavailable` → 503,
   таймаут ack → 504 **без компенсации**;
4. OK → 200 с MissileID.

Списание идёт через `sector.Ordnance.SpendMissile` (app-side адаптер над
`database.TxManager` + `cargo.ConsumeIn`). У ракеты нет собственной строки
в БД (reconstructable, §3), поэтому «транзакция» -- одно списание; важно
именно то, что его делает воркер.

**Порядок в `apply` обязателен** (ЧТЗ-эквивалент AC-3 у торпеды: отказ не
тратит ничего):

```
гейты (владение / не в доке / up_launcher / missileTargetable /
       resolveTargetPos)
  → проверка энергии БЕЗ списания
  → списание боеприпаса (Ordnance, DB)
  → s.missiles[id] = m
  → списание энергии
  → reply
```

До TASK-147 энергия списывалась ДО спавна; теперь -- только после того,
как запуск состоялся, иначе неудачное списание боеприпаса сожгло бы
энергию.

`Ordnance` -- **единственный** путь запуска: воркер без него отвечает
`ErrOrdnanceUnavailable` → 503, а не пускает ракету бесплатно.

### Чем это заменило Consume-before-Send

Раньше handler делал `Consume` до `Send` и `Refund` по `ctx.Done()`.
`AckTimeout` = `TickInterval + 1s`, поэтому задержавшийся тик давал 504 +
возврат ракеты, после чего воркер штатно применял команду: ракета летела,
боеприпас вернулся -- повторяемо (тот же класс дефекта, что TASK-144
закрыла для install-команд).

Следствия нового инварианта:
- 504 означает «исход неизвестен», а не «ничего не произошло»: игрок
  смотрит трюм и повторяет, только если выстрела не было;
- отказ на гейте до списания вообще не касается трюма -- возвращать
  нечего;
- пустой трюм теперь доходит до воркера (осознанная цена, как в
  TASK-144); DB-время идёт через per-drain бюджет `Worker.dbBudget` под
  дедлайном `cfg.RepoTimeout`.

### Тот же инвариант у соседей

TASK-147 применила эту дисциплину ко всему семейству launch-команд, так
что «кто списывает боеприпас» теперь един:

| Команда | Товар | Что в одной транзакции |
|---|---|---|
| `launch-missile` | 10 | только списание (объект RAM-only) |
| `launch-torpedo` | gt23 (кл.2) / gt24 (кл.3) -- маппинг класса живёт в handler'е | списание + `torpedosRepo.WithExecutor(tx).Create` |
| `launch-drone` | 21 (TASK-167 свёл дубликат 51 на каталожный товар) | залп ужимается до остатка товара в трюме ВНУТРИ транзакции (TASK-176), затем списание этого числа + столько же `dronesRepo...Create`. Клампит по `cap up_drone_control - живые` воркер, ДО транзакции, в `LaunchDroneCommand.apply` по RAM сектора (в `app` состояния сектора нет). Короткий трюм укорачивает залп, пустой -- отказ 400 (см. `drones.md` §2.1) |

Из-за этого `TorpedoRepo`/`DroneRepo` в sector несут только
`BatchUpdate`/`Delete` -- пуск в них не пишет. Формулировка ЧТЗ doc-1
FR-003 («при отказе воркера -- рефанд») этим отменена: handler боеприпасом
не владеет, отказ на гейте до списания трюм не касается, а 504 ничего не
компенсирует.

### Методы cargo, участвующие в пути

```go
func ConsumeIn(ctx context.Context, repo Repo, owner EntityRef, gtype GoodsTypeID, qty int64) error
// списание внутри УЖЕ открытой транзакции вызывающего (Service.Consume
// открывает свою, поэтому здесь не подходит).

func RefundIn(ctx context.Context, repo Repo, owner EntityRef, gtype GoodsTypeID, qty int64) error
// зеркало ConsumeIn: начисление внутри УЖЕ открытой транзакции вызывающего,
// без capacity check. Им recall-drones возвращает дронов в трюм одной
// транзакцией с DELETE строк (TASK-152, drones.md §2.2).
// Service-обёртки Refund больше нет: после TASK-152 у неё не осталось ни
// одного продакшн-вызова -- все компенсации живут внутри транзакции вызывающего.
```

## 6. HTTP

### `POST /api/cmd/launch-missile`

Request:
```json
{ "shipID": 17, "targetRef": { "kind": 1, "id": 23 }, "class": 3 }
```

`class` обязателен (1..5). Ноль -- то, что пришлёт клиент, не знающий про
поле, -- отклоняется как невалидный класс, а не молча стреляет «Москитом».

Response (200):
```json
{ "ok": true, "missileID": 42 }
```

Ошибки (сообщения с TASK-185 русские, тело — `{"error": "…"}`):
- `400` некорректный запрос / недопустимый класс ракеты / недопустимая цель
  (не корабль / сам себе) / в трюме нет ракет
- `403` чужой корабль
- `404` корабль не найден / цель не найдена
- `503` сектор занят / служба груза недоступна
- `504` таймаут команды
- `500` внутренняя ошибка сервера (реальная ошибка уходит в лог, не игроку)

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
      "damage": 5000,           // урон класса выпущенной ракеты, см. §3.1
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

- `api.ts`: тип `Missile`, `MissileImpact`, метод
  `sendLaunchMissile(shipID, targetRef, missileClass)`.
- `SectorCanvas`: render слой missiles (точка + короткий хвост по
  direction); render слой missileImpacts (короткая вспышка взрыва,
  один кадр).
- `CombatHUD`: **кнопка на каждый класс** («Ракета: Москит» ... «Ракета:
  Шершень») со своим счётчиком трюма и своим disable-гейтом по нему --
  та же форма, что у двух торпедных кнопок. Селектор класса не заводили:
  боеприпас выбирают по остатку в трюме, а он у каждого класса свой, так
  что счётчик и есть выбор (TASK-175 AC-9).
- `ObjectActionsMenu`: пункт на класс, гейт только по `up_launcher` --
  счётчиков у этого меню нет (своего cargo оно не знает), ровно как у
  торпед. Виден когда цель в наборе ракеты (корабль/статика/ворота/
  контейнер). Цвет акцентный (магента), отличается от «Атаковать
  (лазер)».
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
| LaunchMissile_CopiesClassProfile | spec класса 5 | Damage/Speed/Accel/TurnRate/HitRadius попали в ракету |
| TickMissile_HeavyClassFliesItsOwnProfile | класс 1 и 5 тикают рядом | каждый насыщает СВОЙ Speed; класс 1 ушёл дальше |
| MissileSpecs_MatchCtMissiles | все 5 классов | Damage/Speed/Accel/TTL/TurnRate == `ct_missiles` (числа выписаны из дампа) |
| MissileSpecs_HeavierClassIsSlowerAndHarder | пары соседних классов | Damage ↑, Speed ↓, TurnRate ↓, TTL не убывает |
| MissileSpecs_ReachExceedsRadar | Speed × TTL каждого класса | > 500 (самый широкий радар) -- инвариант калибровки §3.1 |
| DefaultMissileSpec_UnknownClassFallsBack | class 0 / 99 | профиль класса 1, не вырожденный |

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
| LaunchMissile_ClassSelectsProfile | `Class: 5` | ракета в snapshot несёт Damage 25000 / Speed 22 (шов «класс команды → спека») |

### HTTP (`api/launch_missile_test.go`)

| Тест | Запрос | Ожидание |
|---|---|---|
| LaunchMissile_OK | player owns ship, есть missile в cargo, target=ship | 200, cargo Missile -=1 (списание внутри воркера) |
| LaunchMissile_NoCargo | в cargo нет ракет | 400 «в трюме нет ракет» |
| LaunchMissile_NonTargetableKind | target.kind=container | 400, ordnance не вызван |
| LaunchMissile_NotOwner | player A → ship B | 403 |
| LaunchMissile_SectorRejectsKeepsCargo | sector reply Err=ErrInvalidAttackTarget | трюм не тронут (гейт до списания), refund не нужен |
| LaunchMissile_NoOrdnanceWired | воркер без `WithOrdnance` | 503, ракеты нет |
| LaunchMissile_AckTimeoutChargesOnce | ack потерян (504), команда применяется позже | HTTP не делает ни одного cargo-вызова; списано ровно 1, ракета ровно 1; повтор на пустом трюме -- ничего бесплатно |
| LaunchMissile_ClassPicksItsOwnGoods | class 1..5, в трюме только «свой» боеприпас | списан ровно товар 10..14 своего класса И взлетевшая ракета несёт power своего класса |
| LaunchMissile_InvalidClass | class 0 / -1 / 6 / 99 | 400 «invalid missile class», ordnance не тронут |

Плюс в других пакетах: `api.AmmunitionGoodsIDsAreInTheCatalog` (все пять
id есть в `configs/balance.yaml` под своими именами),
`domain.MissileGoodsTypes_AllFiveClassesInOrder`,
`combat.PlanShipDrops_EveryMissileClassRollsSeparately` и
`sector.KillShip_EveryMissileClassCargoBurnsUp` (сгорание стека -- правило
про класс предметов, а не про один товар, см. `kill_object.md` §3).

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
- ~~**Несколько классов ракет** (`ct_missiles`)~~ -- сделано в TASK-175: все
  пять классов провязаны на товары 10-14, спеки в §3.1.
- **Самоуничтожение при `TTL`** не пишет system message игроку (нет
  пока инфраструктуры messages_sys).
- **Friendly fire / hostility relations** — фаза 6.2.
