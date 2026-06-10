# npc_miners — NPC шахтёры (phase 5.4)

Порт miner-ветки `CFabNpcShips` из `starwind/includes/npc_fab_ships.php`
под рантайм AI 5.1 (`ai.Controller`/`WorldView`/`Action`) и кросс-секторную
навигацию (автопилот 2.5 + handoff 2.4/5.3). Не литеральный порт state-машины
на пять статусов с буром через `TO_ShipMovement`/`UseDrill`, а сведённая
реактивная реимплементация под state-in-RAM и typed `EntityRef`.

## Что в оригинале

`CFabNpcShips::ProcessTL()` крутит Mining-TS (`ship_class=2`): корабль с буром
(`up_drill`), приписанный к фабрике-владельцу. Статусы:
`IDLE → FLYING_TO_ASTEROID → MINING → FLYING_TO_FACTORY → UNLOADING → IDLE`.

- **IDLE** (`minerIdle`): фабрика стоит у дока, трюм пуст. Определяет самую
  дефицитную руду фабрики (`goods_type IN (8,9)`, запас < одного цикла),
  ищет ближайший астероид нужного типа (`findNearestAsteroid` через
  `CFabPathRouter::hops` в радиусе `miner_search_radius`), летит к нему.
  Нет руды-потребности или астероида — `snoozeIdle` (пропуск тиков).
- **FLYING_TO_ASTEROID** (`minerArriveAsteroid`): ведёт через ворота; к
  астероиду не пристыковаться — прибытие по `dx²+dy² ≤ 50²`, тогда `attack_type=1`
  (бур) → MINING. Если астероид исчез — ищет следующий.
- **MINING** (`minerMining`): `TO_ShipMovement` дёргает `UseDrill` — руда
  падает в **контейнеры** (`up_drill` model), которые корабль потом
  подбирает. Трюм полон или астероид (`mass<=0`) выработан → FLYING_TO_FACTORY.
- **FLYING_TO_FACTORY / UNLOADING**: довозит руду, перекладывает весь груз на
  фабрику, → IDLE.

`asteroids(ID, type, mass, density, sector, pos_x, pos_y)`; `mass` убывает от
бурения, `mass<=0` → астероид удаляется (руда выпала контейнерами).

## MVP в Go (это 5.4)

Каждый шахтёр — **независимый реактивный контроллер** на рантайме 5.1, как
трейдер 5.3, плюс фаза добычи. Дом-фабрика и тип руды зафиксированы при спавне;
текущий целевой астероид и счётчик добытого — в `ai_state` JSON.

### Упрощения относительно SP

- **Бур без промежуточных контейнеров.** Старая «руда → контейнер → подбор»
  цепочка — артефакт связки `TO_ShipMovement`+`UseDrill`. Здесь действие
  `ai.Mine{Asteroid, Amount}` за один тик списывает `Amount` массы астероида и
  кладёт `Amount` руды в трюм корабля напрямую (worker, один writer). Объём
  ограничивается фактической массой астероида (capacity-гард в `cargo.Add`).
- **Загрузка по счётчику, а не по заполнению трюма.** Контроллер не видит
  содержимое трюма (`WorldView` его не отдаёт). Поэтому он сам считает добытое
  (`mined += DrillRate`) и уходит домой при `mined >= LoadTarget` или истощении
  астероида. `LoadTarget` выставлен заведомо ниже ёмкости трюма (`cargobay=100`,
  руда `space=2` → ≤50 ед.; `LoadTarget=40`), чтобы `cargo.Add` не упёрся в
  `ErrNoSpace`.
- **Руда = первый вход рецепта** (`recipe.Inputs[0].GoodsType`), симметрично
  трейдеру (везёт `Outputs[0]`). `Asteroid.OreType` — это сам `GoodsTypeID`
  (без таблицы «asteroid type → goods», как было `gt = type+7`).
- **Idle-перепоиск — только в своём секторе** (`WorldView.Asteroids()`).
  Первичный кросс-секторный выбор астероида делает спавнер через
  `PathRouter.Hops` (у контроллера роутера нет). На сид-мире астероид в том же
  секторе, что и фабрика, — перепоиск работает полностью.

### Машина состояний контроллера (`ai/miner`)

`Phase ∈ {to_asteroid, mining, to_home, idle}`; state: `Home Leg`, `Ore`,
`Target {ID, Sector, Pos}`, `Mined`.

- **to_asteroid**: цель в другом секторе или вне `MineRange` → `SetCourse` к
  `Target.Pos` (автопилот + handoff). Астероид исчез (нет в `Asteroids()` своего
  сектора или `Mass<=0`) → перепоиск локально; нет — домой. В пределах
  `MineRange` → `mining`.
- **mining**: астероид исчез/`Mass<=0` или `Mined>=LoadTarget` → `to_home`.
  Снесло за `MineRange` → назад в `to_asteroid`. Иначе `Mine{Target.ID, DrillRate}`,
  `Mined += DrillRate`. (Mine-apply гасит `Target/FinalTarget` — корабль стоит.)
- **to_home**: пришвартован к фабрике (как `trader.arrived`) → `Transfer`
  (трюм → фабрика, весь груз), `Mined=0`, `idle`. Иначе `SetCourse(home,
  Approach=home.Ref)`.
- **idle** (у фабрики): ближайший живой астероид нужной руды в своём секторе →
  `Target`, `Mined=0`, `to_asteroid`. Нет — `Idle` (стоит).

### Worker / sector

- `domain.Asteroid{ID, SectorID, Pos, Mass, OreType}`; `AsteroidID`.
- `sectorState.asteroids` (map), грузятся на cold-start (`asteroids.LoadAll`),
  периодический `BatchUpdate` массы (как дроны) + `Delete` при `Mass<=0`
  (immediate). `WorldView.Asteroids()` отдаёт копии.
- `ai.Mine` применяется методом воркера: lookup астероида в state, кап по массе,
  `MinerLogistics.AddOre(ship, ore, amount)` (поверх `cargo.Service.Add` с
  capacity-гардом), декремент массы; `Mass<=0` → удаление (RAM + repo.Delete).
  Гасит `Target/FinalTarget`, чтобы корабль держал позицию у астероида.
- Выгрузка домой — переиспользует `ai.Transfer` (5.3, `traderLogistics.Haul`).

### Спавн (cold-start)

Расширение `app/npc_spawner.go`: для каждой производящей фабрики с непустыми
`recipe.Inputs`, если есть достижимый астероид руды `Inputs[0]` — спавнит
`MinersPerFactory` (default 1) шахтёров. Ближайший астероид выбирается по
`PathRouter.Hops` среди секторов с такой рудой. Идемпотентность — колонка
`npc_ships.controller_kind` ('trader'/'miner'); `CountByHome` группирует по
(home, kind), так что трейдер и шахтёр на одной фабрике не вытесняют друг друга.

## Покрытие тестами

- `TestUnit_Miner_*` — машина состояний: подлёт, добыча, истощение → домой,
  выгрузка, idle-перепоиск, idle без астероидов.
- `TestUnit_Worker_Miner_MinesAndUnloads` — e2e в секторе: подлёт к
  засеянному астероиду, убыль массы, удаление при 0, руда в трюме, возврат,
  выгрузка на фабрику.
- `TestUnit_NPCSpawner_Miners_*` — выбор руды (`Inputs[0]`) и ближайшего
  астероида по hops; пропуск при отсутствии астероида.
- Integration (`asteroids` repo, npc_ships kind) — локально не гоняется
  (testcontainers, 7.6); миграция 0025 верифицируется на реальном PG16.

## Отложено (вне scope 5.4)

- Фронт (астероиды на канвасе) — Components задачи: back, db.
- Кросс-секторный idle-перепоиск (нужен роутер у контроллера).
- Несколько входов рецепта (шахтёр везёт только `Inputs[0]`).
- Контейнерный дроп руды, density, выбор «самой дефицитной» руды по запасу
  фабрики, respawn при гибели, acceptance-паритет с MySQL (7.6a — это не
  боевой SP).

## Ссылки

- Старая логика: `starwind/includes/npc_fab_ships.php` (miner-ветка)
- CLAUDE.md старого проекта: «findNearestAsteroid использует роутер»,
  «Колонки с packed full_id vs plain ID»
- Рантайм 5.1: `back/internal/ai/`; трейдер 5.3: `back/internal/ai/trader/`
