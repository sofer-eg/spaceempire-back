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
К `Idle`/`MoveTo` добавляется `Attack{Target EntityRef}`. Семантика
применения в воркере (`applyAIAction`) — «намерение корабля на тик»:
- `MoveTo{p}` → `Target=p`, `AttackTarget=nil` (лететь, не драться).
- `Attack{ref}` → `AttackTarget=ref` и `Target=` позиция цели (сближение +
  огонь; огонь по `AttackTarget` уже реализован в 4.2 `fireLasers`).
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
warship-классом (`Config.WarshipClassIDs`; в проде — M3/M4/M6, тот же набор,
что спавнит `race_fleet_spawner`), включая себя. Фильтр по классу обязателен:
`npc_spawner` присваивает `Race` и мирным TS/шахтёрам/пассажирам — по одной
расе они в звено не попадают. `ShipClassID == 0` (скафандр/legacy) — не
warship. Если сам корабль не warship (или `WarshipClassIDs` пуст) — звена
нет, поведение одиночное (до-TASK-66). Лидер = минимальный `ShipID` звена.

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
`Order=Retreat`, `MoveTo(anchor)` — даже при полном HP. Когда врага в
радиусе нет — peak ребейзится к текущему alive (невосполнимые потери не
запирают звено в вечном ретрите), звено возвращается к патрулю/строю.
Старый `ai_state` без `squadPeak` совместим: 0 → пик поднимется на первом
тике с врагом.

**Приоритеты на тике.** Враг рядом: личный flee по HP → групповой ретрит по
потерям → engage с focus-fire (без изменений). Врага нет: ребейз пика →
патруль (лидер/одиночка) или строй (ведомый).

Focus-fire (`allyFocusTarget`) намеренно шире звена: ally = любой
не-враждебный по `Targeter` (расы, невраждебные друг другу, сходятся на
одной цели). Строй и отступление — строго по своей расе.

### Wiring
`app.go`: строит `relations.Service` + `Precount`, адаптер `Targeter`
(9.1: `raceMatrixTargeter` по `DefaultStanding`; 9.4: поверх —
`wantedOverlayTargeter`), вычисляет `WarshipClassIDs` из
`shipClasses.AllShipClasses()` (Class ∈ {3,4,6}) и вызывает
`race.Register(registry, targeter, race.Config{WarshipClassIDs: …})`.

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
  (пик переживает rebuild из `ai_state`); ребейз пика после ухода врага —
  `TestUnit_Race_FollowerHoldsFormationOffset` / `LeaderPatrolsWithFollowers`
  / `CivilianSameRaceNotInSquad` / `SquadRetreatsOnLosses` /
  `PeakRebasesAfterEnemyLeaves`.
- Реакция end-to-end на рантайме: sector-тест (race-контроллер через
  реестр → `AttackTarget` выставлен, враг получает урон).
