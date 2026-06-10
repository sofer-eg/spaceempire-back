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

### Координация (FleetAI)
MVP: координации уровня флота/группы (строй, `pos_in_order`, общий target)
**нет** — корабли реагируют независимо. Per-sector FleetAI-координатор,
`f_flightgroups`-строй и трансляция приказов флот→группы — отложено.

### Wiring
`app.go`: строит `relations.Service` + `Precount`, адаптер `Targeter` над
ним, `race.Register(registry, targeter)`, передаёт непустой реестр в
`WithAI` (был пустой с 5.1). Так relations 6.2 получает первого потребителя.

## Не входит в 5.2 (отложено)
- Fleet/FlightGroup-строй и координатор (богатая FleetAI).
- Race-standing матрица (`race_relations`) — когда расы станут relation-
  endpoint'ами; сейчас враждебность через инъектируемый `Targeter`.
- Спавн живых расовых NPC в секторы (нужен NPC-owner: `ships.player_id` —
  `NOT NULL`) — инфраструктура NPC-спавна (5.3+). Поведение RaceAI
  доказывается unit/sector-тестами на рантайме 5.1.
- Ракеты/дроны от AI, gate-pass межсекторно — позже.

## Критерии (проверяется тестами)
- Расовый корабль патрулирует без врага, атакует враждебного, отступает при
  низком HP — unit-тесты контроллера.
- Не атакует не-враждебных (союзников) — unit-тест с `Targeter`=false.
- Реакция end-to-end на рантайме: sector-тест (race-контроллер через
  реестр → `AttackTarget` выставлен, враг получает урон).
