# race_ai — Race AI / FleetAI (phase 5.2)

Порт идеи `process_full_ai.php` / `CFleet::Turn` под рантайм AI 5.1
(`ai.Controller`/`WorldView`/`Action`) и систему отношений 6.2
(`relations.Service.IsHostile`). Не литеральный порт битфлагов/`f_ships` —
чистая реимплементация под state-in-RAM и typed `EntityRef`.

## Что в оригинале

`ProcessAINPC` крутит по расам (1–8: Argon, Boron, Paranid, Split, Teladi,
Pirate, Xenon, Khaak). `CState::Turn` → `CFleet::Turn` на каждом тике:
сканирует радар флота, переводит приказы флота в приказы групп
(`f_flightgroups`), для каждой группы выбирает цель по угрозе, выстраивает
строй (`pos_in_order`), двигает корабли. Приказы (`Orders`) — битовая маска
(`engage`, `attack`, `patrol`, `full_retreat`, `goto_sector`, …). Реакция:
враг в радаре + `engage` → `attack`; HP группы < `OPT_EVADE_MODE_ENTER`
(~30 %) → режим эвейда/отступление.

Таблицы `f_ships(race, FleetAssignment, GroupAssignment, ship_id, Orders,
Status, pos_in_order)` и `f_flightgroups(LeaderID, target_id, patrol_*, …)`
держат назначения. Всё на packed `full_id` и временных таблицах.

## MVP в Go (это 5.2)

Каждый расовый корабль — **независимый реактивный контроллер** на рантайме
5.1. Приказ (`Order`) — состояние контроллера, не битовая маска.

### `ai.Action` (расширение 5.1)
К `Idle`/`MoveTo` добавляется `Attack{Target EntityRef}` и (TASK-208)
`MoveAndFire{Target Vec2, Fire EntityRef}`. Семантика применения в воркере
(`applyAIAction`) — «намерение корабля на тик»:
- `MoveTo{p}` → `Target=p`, `AttackTarget=nil` (лететь, не драться).
- `Attack{ref}` → `AttackTarget=ref` и `Target=` позиция цели (сближение +
  огонь; огонь по `AttackTarget` уже реализован в 4.2 `fireLasers`).
- `MoveAndFire{p, ref}` → `Target=p` И `AttackTarget=ref` — «отступление с
  отстрелом»: лететь к СВОЕЙ точке (в отличие от `Attack`, `Target` — не
  позиция жертвы), продолжая стрелять по `ref`. Новой боевой механики не
  требуется: `fireLasers` бьёт по `AttackTarget` без требования к ориентации
  носа — гейты только дальность (`combat.FireLaser`) и friendly-fire.
- `Idle`/nil → no-op.

### `internal/ai/race`
`Controller` (kind `"race"`), состояние в `ai_state` JSON:
`{race, order, anchorX, anchorY, phase}`. Зависимость — `Targeter`
(инъекция): `IsHostile(self, other domain.Ship) bool` (в проде — адаптер
над `relations.Service`; в тестах — fake).

`Tick(view)`:
1. Ближайший враждебный корабль в `DetectionRange` (по `view.Ships()` +
   `Targeter`).
2. `hp = Self.HP / Self.MaxHP`.
3. Решение:
   - враг рядом и `hp < FleeThreshold` (0.3) → `Order=Retreat`, `MoveTo`
     прочь от врага (к anchor) — порог эвейда из оригинала;
   - враг рядом → `Order=Engage`, `Attack(ближайший)`;
   - иначе → `Order=Patrol`, `MoveTo` по кругу вокруг anchor.

«Не атакует союзников» — контроллер выдаёт `Attack` только когда
`Targeter.IsHostile` истинно (критерий приёмки).

### Координация: эмерджентные звенья (8.4 / TASK-66)
Формального координатора и таблиц-аналогов `f_ships`/`f_flightgroups` нет —
звено выводится каждым контроллером per-tick из `view.Ships()`, разделяемое
состояние не нужно.

**Звено (squad).** Живые корабли сектора с `Ship.Race == self.Race` И
warship-классом (`Config.WarshipClassIDs`), включая себя. Единый источник
набора боевых классов (TASK-207) — `balance.ShipClass.IsWarship`:
`Class ∈ {2,3,4,5,6}` (M2/M3/M4/M5/M6). Членство в звене шире спавн-наборов
спавнеров (нава спавнит {3,4,6}, инвазии — ксеноны {3,4,5} и каак {2,3}) —
спавн-фильтры это спавн-политика и остаются своими литералами, но class-5
ксеноны и class-2 каак входят в звенья своих групп. Фильтр по классу
обязателен: `npc_spawner` присваивает `Race` и мирным TS/шахтёрам/пассажирам
— по одной расе они в звено не попадают. `ShipClassID == 0`
(скафандр/legacy) — не warship. Если сам корабль не warship (или
`WarshipClassIDs` пуст) — звена нет, поведение одиночное (до-TASK-66).
Лидер = минимальный `ShipID` звена.

**Standalone-контроллеры (TASK-207).** В `ai_state` — флаг `standalone`
(`json:"standalone,omitempty"`; старый снапшот без ключа → `false`,
обычный член звена). Ставится конструктором
`race.NewInitialStandaloneState(raceID, anchor)`; используется
`quest_spawner` для квестовых NPC (эскортируемый торговец / цель защиты) —
иначе warship-класс той же расы, что нава сектора, становился бы ведомым и
улетал от игрока в клин-офсет к лидеру навы. При `standalone` `squad()`
возвращает `nil`: корабль ведёт себя соло как до TASK-66 — патруль вокруг
своего anchor, engage с focus-fire, личный flee по HP; строй и групповой
ретрит к нему не применяются. Нава (`race_fleet_spawner`) и инвазии
(`invasion_spawner`) — обычный `NewInitialState`: звенья внутри инвазионной
группы желательны.

**Принятая асимметрия (TASK-207).** Членство других кораблей вычисляется из
видимых полей `domain.Ship` (Race + класс) — поле-маркер на `domain.Ship`
сознательно не заводится (4 слоя провязки + ловушка handoff-survival).
Поэтому нава по-прежнему считает standalone-корабль своей расы в своём
звене/`squadPeak`. Это принятый мелкий шум: пик самокорректируется ребейзом,
как только врага в радиусе нет.

**Строй в патруле** (наследник `pos_in_order`). Лидер и одиночный корабль
патрулируют круг вокруг anchor как раньше. Ведомый ранга `r` (1-based позиция
в отсортированном по ID звене) летит `MoveTo(позиция лидера + офсет)`: клин с
чередованием сторон, ряд `row = (r+1)/2`, офсет
`(-row·FormationSpacing, ±row·FormationSpacing)` (r нечётный → +Y, чётный →
−Y). «Позади» — мировой −X: строй детерминирован и не зависит от курса
лидера. `FormationSpacing` — Config, default 50.

**Групповое отступление.** В `ai_state` — `squadPeak`: максимальная живая
численность звена, виденная при враге в радиусе (`peak = max(peak, alive)`
каждый такой тик). Если враг в `DetectionRange` И
`alive < peak × SquadRetreatFraction` (Config, default 0.5) →
`Order=Retreat`, `MoveAndFire(anchor, ближайший враг)` — даже при полном HP:
корабль отходит к якорю, отстреливаясь от преследователя (TASK-208 — враг,
кемпящий якорь, не фармит NPC бесплатно). Личный flee по
`hp < FleeThreshold` остаётся чистым `MoveTo` (паническое бегство без огня)
и имеет приоритет над групповым ретритом. Старый `ai_state` без `squadPeak`
совместим: 0 → пик поднимется на первом тике с врагом.

**Гистерезис ребейза пика (TASK-208).** Peak ребейзится к текущему alive
только после `PeakRebaseTicks` (Config, default 10) ПОСЛЕДОВАТЕЛЬНЫХ тиков
без врага в `DetectionRange`. Счётчик — `noEnemyTicks` в `ai_state`
(`omitempty`; старый JSON без ключа → 0), инкрементируется в тик без врага
(клампится на `PeakRebaseTicks`), любой тик с врагом сбрасывает в 0. До
гистерезиса peak схлопывался мгновенно: отход выводил врага за радиус на
один тик → пик = alive → враг снова в радиусе → одиночка разворачивался на
превосходящую силу (кайт-осцилляция retreat↔engage на границе радиуса).
Невосполнимые потери по-прежнему не запирают звено в вечном ретрите — просто
ребейз наступает через N спокойных тиков.

**Приоритеты на тике.** Враг рядом: сброс `noEnemyTicks` → личный flee по HP
(чистый `MoveTo`) → групповой ретрит по потерям (`MoveAndFire`) → engage с
focus-fire. Врага нет: инкремент `noEnemyTicks`, ребейз пика после
`PeakRebaseTicks` → патруль (лидер/одиночка) или строй (ведомый).

Focus-fire (`allyFocusTarget`) намеренно шире звена: ally = любой
не-враждебный по `Targeter` (расы, невраждебные друг другу, сходятся на
одной цели). Строй и отступление — строго по своей расе.

### Wiring
`app.go`: строит `relations.Service` + `Precount`, адаптер `Targeter`
(9.1: `raceMatrixTargeter` по `DefaultStanding`; 9.4: поверх —
`wantedOverlayTargeter`), вычисляет `WarshipClassIDs` через
`raceWarshipClassIDs(shipClasses)` (`balance.ShipClass.IsWarship`,
Class ∈ {2,3,4,5,6}) и вызывает
`race.Register(registry, targeter, race.Config{WarshipClassIDs: …})`.
`quest_spawner` пишет `ai_state` через `NewInitialStandaloneState`;
`race_fleet_spawner` и `invasion_spawner` — обычный `NewInitialState`.

## Отложено
- Персистентный per-sector FleetAI-координатор и формальные
  `f_ships`/`f_flightgroups`-структуры (общая цель уровня флота, трансляция
  приказов флот→группы) — текущие звенья эмерджентные, per-tick.
- Строй с учётом курса лидера (сейчас клин по мировой оси −X).
- Ракеты/дроны от AI, gate-pass межсекторно — позже.

## Критерии (проверяется тестами)
- Расовый корабль патрулирует без врага, атакует враждебного, отступает при
  низком HP — unit-тесты контроллера.
- Не атакует не-враждебных (союзников) — unit-тест с `Targeter`=false.
- Focus-fire: предпочитает цель союзника, fallback на ближайшую —
  `TestUnit_Race_FocusFiresAllyTarget` / `FocusFallsBackToNearest`.
- Звенья (TASK-66): ведомый держит строй-офсет; лидер патрулирует как
  раньше; мирный той же расы не в звене; групповой ретрит при потере доли
  (пик переживает rebuild из `ai_state`) —
  `TestUnit_Race_FollowerHoldsFormationOffset` / `LeaderPatrolsWithFollowers`
  / `CivilianSameRaceNotInSquad` / `SquadRetreatsOnLosses`; ведомый при враге
  в радиусе атакует, а не строится; чередование сторон клина rank ≥ 2;
  мёртвый союзник (HP ≤ 0) вне звена —
  `TestUnit_Race_WingmanEngagesHostileNotFormation` /
  `WedgeAlternatesSidesByRank` / `DeadAllyExcludedFromSquad`.
- Гистерезис и отступление с отстрелом (TASK-208): групповой ретрит =
  `MoveAndFire` с верными Target (якорь) и Fire (ближайший враг); ретрит
  держится при мигании врага в/из радиуса; ребейз после N спокойных тиков
  (счётчик переживает rebuild из `ai_state`); тик с врагом сбрасывает
  счётчик; личный flee остаётся чистым `MoveTo` —
  `TestUnit_Race_SquadRetreatsOnLosses` / `RetreatHoldsThroughEnemyFlicker` /
  `PeakRebasesAfterQuietTicks` / `EnemyTickResetsRebaseCounter` /
  `PersonalFleeStaysPureMoveTo`; воркер-семантика `MoveAndFire` (waypoint =
  своя точка, огонь идёт, корабль отходит) —
  `TestUnit_Worker_AIMoveAndFire_RetreatsWhileFiring` (sector).
- Standalone (TASK-207): не становится ведомым при однорасовом лидере с
  меньшим ID (флаг переживает rebuild из `ai_state`); не уходит в групповой
  ретрит; личный flee сохранён; старый `ai_state` без ключа → обычный член
  звена — `TestUnit_Race_StandaloneNotWingman` / `StandaloneNoGroupRetreat`
  / `StandaloneKeepsPersonalFlee` / `OldStateWithoutStandaloneJoinsSquad`.
- Единый набор боевых классов: `TestUnit_ShipClass_IsWarship` (balance,
  M2..M6) и `TestUnit_RaceWarshipClassIDs` (app: class-5/class-2 в наборе
  членства, TS/M1 — нет).
- Реакция end-to-end на рантайме: sector-тест (race-контроллер через
  реестр → `AttackTarget` выставлен, враг получает урон).
