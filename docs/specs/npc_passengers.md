# npc_passengers — NPC пассажирские TS (phase 5.5)

Порт passenger-ветки `CFabNpcShips` из `starwind/includes/npc_fab_ships.php`
(+ `starwind/docs/npc_passenger_transport.md`) под рантайм AI 5.1
(`ai.Controller`/`WorldView`/`Action`) и кросс-секторную навигацию
(автопилот 2.5 + handoff 2.4/5.3). Сведённая реактивная реимплементация под
state-in-RAM и typed `EntityRef`.

## Что в оригинале

`CFabNpcShips::ProcessPassenger()` крутит пассажирские TS (`ship_class=3`),
приписанные к `trade_stations` рас 1-4. Статусы
`IDLE → FLYING/RETURNING → IDLE`:

- **IDLE** (`generatePassengerRoute`): стоит на станции; ждёт
  `passenger_dock_wait_seconds` (через `fab_npc_ships.next_action` DATETIME);
  по истечении выбирает **случайную** `stations`/`trade_stations` расы 1-4 в
  радиусе `passenger_route_radius` hops **от текущего сектора** (drift!), грузит
  `passengers = rand(1, floor(cargobay/3))`, летит. Если ушёл дальше
  `passenger_home_max_hops` от home — форсированный возврат на home TS.
- **FLYING/RETURNING** (`passengerAdvance`): ведёт через ворота; при прибытии
  `passengers = 0`, → IDLE с задержкой стоянки.

Пассажиры — колонка `ships.passengers`. При гибели — дроп «Рабы» (это 5.6).

## MVP в Go (это 5.5)

Каждый пассажирский TS — независимый реактивный контроллер на рантайме 5.1.
Дом-TS и **пул** соседних станций фиксируются при спавне; «фаза» и таймер
стоянки — в `ai_state` JSON.

### Машина состояний (`ai/passenger`)

`Phase ∈ {flying, waiting}`; state: `Pool []Leg`, `Dest Leg`, `RouteIdx int`,
`WaitLeft int`.

- **flying**: пришвартован к `Dest` (как `trader.arrived`) → `DropPassengers`,
  `WaitLeft = DockWaitTicks`, → waiting. Иначе `SetCourse(Dest, Approach=Dest.Ref)`.
- **waiting**: `WaitLeft>0` → `WaitLeft--`, `Idle`. Иначе round-robin
  `pickNext` (следующий пул-Leg ≠ текущий) → `Dest`, → flying, `BoardPassengers`.
  Нет других целей → `Idle` (стоит).

`BoardPassengers`/`DropPassengers` применяет воркер (single writer, есть RNG):
board пишет `ship.Passengers = rollPassengers(rng, Max)` (RNG — `combat.RNG.Float64`),
drop — `0`; оба immediate-Save (поле персистентно для 5.6).

### Упрощения относительно SP

- **Пул фиксирован при спавне и home-относителен.** Контроллеру недоступны
  роутер и статика чужих секторов (`WorldView` отдаёт только свой сектор).
  Спавнер (есть роутер + вся статика) собирает пул = `stations`/`trade_stations`
  рас 1-4 в радиусе `passenger_route_radius` hops **от home** (home включён),
  кладёт в `ai_state`. Контроллер курсирует пул **round-robin** (не DB-random),
  с per-ship стартовым `RouteIdx` для разброса. Дрейфа за home нет →
  `passenger_home_max_hops` неприменим (убран).
- **Стоянка — в тиках, не по wall-clock.** У контроллера нет часов; вместо
  `fab_npc_ships.next_action` DATETIME — счётчик `WaitLeft` тиков. App
  переводит `passenger_dock_wait_seconds` (50) в тики по `TickInterval`
  (`ceil`). Тест проверяет соблюдение, тикая N раз (FakeClock не нужен).
- **`passengers = 1 + rand·Max`** (Max ≈ `floor(cargobay/3)` для дефолтного
  трюма 100 → `passengerMaxOnBoard=33`), без поля `Ship.Cargobay` (ёмкость
  живёт в cargo-repo).

### Домен / схема

- **`ships.passengers INTEGER NOT NULL DEFAULT 0`** (миграция `0026`),
  `domain.Ship.Passengers`. LoadAll читает, Save пишет (board/drop — discrete
  immediate-события; периодический BatchUpdate поле не трогает). У игроков 0.

### Спавн (cold-start)

`app/npc_spawner.go`: на каждую `trade_stations` расы 1-4 спавнит
`PassengersPerTradeStation` (default 5) TS; пул — `passengerPool(home.sector)`.
Пропуск (лог), если в пуле нет цели кроме home. Идемпотентность — колонка
`npc_ships.controller_kind` ('passenger'); `CountByHome` по (home, kind).
На сиде 3 TS рас 1,1,2 → 15 пассажирских TS.

## Покрытие тестами

- `TestUnit_Passenger_*` — машина: board-on-depart, course-while-flying,
  drop-on-arrival, wait→next-dest, stays-when-no-dest, rebuild.
- `TestUnit_Worker_Passenger_FerriesAndBoards` — e2e: board (passengers>0) →
  fly → drop (0) у дальней станции (через immediate-Save trail).
- `TestUnit_NPCSpawner_PassengerPool_*` / `CollectPassengerDests` /
  `PoolHasOtherThan` — фильтр радиуса/расы, флэттен, guard.
- Миграция `0026` верифицирована на PG16 (NOT NULL DEFAULT 0).

## Отложено (вне scope 5.5)

- Дроп «Рабы» при гибели — это 5.6.
- Случайный (DB-random) выбор цели относительно текущего сектора и `home_max_hops`
  возврат (заменены home-относительным round-robin пулом).
- `Ship.Cargobay` (Max пассажиров — конфиг, не `cargobay/3`).
- Фронт — Components задачи: back, db.

## Ссылки

- `includes/npc_fab_ships.php` — passenger-ветка
- `starwind/docs/npc_passenger_transport.md`
- CLAUDE.md старого проекта: «NPC пассажирские перевозки»
- Рантайм 5.1: `back/internal/ai/`; трейдер 5.3 / майнер 5.4 как образец
