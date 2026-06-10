# npc_traders — NPC TS-трейдеры (phase 5.3)

Порт идеи `CFabNpcShips` (trader-ветка) из `starwind/includes/npc_fab_ships.php`
под рантайм AI 5.1 (`ai.Controller`/`WorldView`/`Action`) и кросс-секторную
навигацию (автопилот 2.5 + handoff 2.4). Не литеральный порт `fab_npc_ships`
state-машины на 10 статусов — сведённая реактивная реимплементация под
state-in-RAM и typed `EntityRef`.

## Что в оригинале

`CFabNpcShips::Turn()` крутит TS-корабли, привязанные к фабрике: при нехватке
сырья летят к фабрике-поставщику, закупают (без списания кредитов — это
внутрикорпоративная логистика), везут на фабрику-владельца, выгружают; в
режиме продажи возят продукцию на торговую станцию. Межсекторный перелёт —
через `CFabPathRouter` (BFS по воротам), стыковка через `SP Docking`,
спавн замены при потере (`SPAWNING`). Идентичность NPC хранится в таблице
`fab_npc_ships(ship_id, owner_type, owner_id, target_fab, next_action, …)`;
`ships.owner = 0` маркирует «бесхозный» NPC-корабль, исключённый из флот-AI
через `DELETE FROM f_ships`.

Подробный дизайн — `starwind/docs/npc_traders.md`.

## MVP в Go (это 5.3)

Каждый трейдер — **независимый реактивный контроллер** на рантайме 5.1.
Маршрут — два конца (home-фабрика ↔ dest-торговая станция) и один тип груза,
зафиксированные при спавне; «фаза» — состояние контроллера в `ai_state` JSON.

### NPC-владелец

`ships.player_id NOT NULL REFERENCES players(id)` — поэтому вводится
**системный игрок `__npc__`** (seed-миграция, `password_hash='!'` —
невалидный bcrypt, залогиниться нельзя). Все NPC-корабли принадлежат ему.
Отношения по умолчанию Neutral (6.2) → игроки/башни/race-AI не атакуют
трейдеров автоматически; трейдер сам никогда не выставляет `AttackTarget`.

`FleetAI`-перехвата (5.2 race) не существует: race-контроллер привязывается
только к кораблям с `ai_state.controller_kind = "race"`, трейдер —
`"trader"`. Разные kind'ы в реестре → критерий «не попадают в FleetAI»
выполняется конструктивно, без аналога `DELETE FROM f_ships`.

### Идентичность: таблица `npc_ships`

Минимальный порт `fab_npc_ships`: `npc_ships(ship_id PK → ships ON DELETE
CASCADE, home_kind SMALLINT, home_id BIGINT)`. Назначение:
- идемпотентный спавн (не плодить трейдеров на каждом рестарте — спавним
  только для home-фабрик, которых ещё нет в `npc_ships`);
- связь корабль → home-фабрика (для отладки/будущего respawn).
Маршрут/фаза/груз живут в `ai_state` JSON, не дублируются здесь.

### Кросс-секторная навигация

Трейдер **не** двигает корабль вручную между секторами. Он ставит
`ship.FinalTarget = Course{Sector, Pos, Approach=stationRef}` (новое действие
`ai.SetCourse`). Дальше работает существующая машинерия игрока:
- `resolveAutopilot` (2.5) hop-by-hop ведёт корабль к воротам в сторону
  целевого сектора, в целевом секторе паркует у станции на `DockRange/2`;
- `tryAutoJump` (2.4) выполняет прыжок через ворота — **уже работает для
  любого корабля с `FinalTarget`, не только игрока**.

Чего не хватало и что добавляется в 5.3 — **handoff AI-контроллера**
(отложено в 5.1): при прыжке `executeJump` маршалит контроллер корабля,
кладёт `ControllerKind`/`ControllerState` в `JumpEvent`, переносит строку
`ai_state` на target-сектор; в целевом секторе `JumpIntakeCommand`
восстанавливает контроллер через реестр и кладёт в `s.controllers`. Без
этого трейдер «забывал» бы маршрут после первого прыжка.

### `ai.Action` (расширение 5.1)

К `Idle`/`MoveTo`/`Attack` добавляются:
- `SetCourse{Course}` → воркер пишет `ship.FinalTarget`, чистит `Target`
  (пусть автопилот пересчитает), `CurrentTargetRef = Course.Approach`,
  сбрасывает `AttackTarget`.
- `Transfer{From, To EntityRef, GoodsType, MaxUnits}` → воркер выполняет
  перенос груза через инъектируемый `TraderLogistics.Haul` (DB-транзакция
  in-tick — как `production.Tick`). Haul переносит `min(есть_у_источника,
  MaxUnits, влезает_в_приёмник)` единиц; 0 → no-op. Логистика —
  **без денег** (как «BUYING без списания кредитов» в оригинале): груз
  ходит по таблице `cargo` между `EntityRef` фабрики и корабля.

`applyAIAction` становится методом воркера (`w.applyAIAction(ctx, …)`),
т.к. `Transfer` нуждается в ctx и зависимости.

### `internal/ai/trader`

`Controller` (kind `"trader"`), состояние в `ai_state` JSON:
`{phase: "home"|"dest", home:{sector,x,y,ref}, dest:{sector,x,y,ref},
goods, haulQty}`. Координаты обоих концов хранятся в state, т.к.
`WorldView` отдаёт статику только текущего сектора — для `SetCourse` в
чужой сектор нужны заранее известные коорд.

`Tick(view)` по текущей фазе (целевой конец `leg`):
1. **arrived** := `Self().SectorID == leg.Sector && dist(pos, leg.pos) <=
   ArriveRadius && Self().Vel.IsZero()` (автопилот паркует с `Vel=0` на
   прошлом тике; `tickAI` идёт до автопилота, так что видим прошлое
   состояние).
   - фаза `home` + arrived → `Transfer{From=home, To=ship, goods, haulQty}`,
     фаза := `dest`.
   - фаза `dest` + arrived → `Transfer{From=ship, To=dest, goods, haulQty}`,
     фаза := `home`.
2. иначе, если `FinalTarget` пуст или указывает не на `leg` →
   `SetCourse{leg course}`.
3. иначе (летит/паркуется) → `Idle`.

Контроллер чистый: сам перенос делает воркер, контроллер только решает.

### Спавн (cold-start)

`npcSpawner.EnsureSpawned` в `app.Run` **до** `LoadAll` секторов:
1. Найти/создать игрока `__npc__`.
2. Кандидаты home — `stations` с рецептом (`balance.Recipe(type)` есть);
   груз = `recipe.Outputs[0].GoodsType`.
3. Для каждой home-фабрики, ещё не представленной в `npc_ships`:
   dest = ближайшая достижимая `trade_station` (по `PathRouter.Hops`;
   может быть в том же секторе). Нет достижимой → пропустить.
   Создать `cfg.TradersPerFactory` (default 1) кораблей: строка `ships`
   (владелец `__npc__`, позиция у home, ТТХ как у стартового корабля),
   строка `ai_state` (kind `trader`, JSON с маршрутом, sector=home.sector),
   строка `npc_ships`.
4. Воркеры на старте через `LoadAll` поднимут корабли и контроллеры.

Идемпотентность: per-home через `npc_ships`. На сид-мире (фабрика —
только station id=1, sector 1) живёт один трейдер sector 1 → ближайшая
trade station тоже sector 1, т.е. вживую маршрут внутрисекторный.
Кросс-секторность доказывается отдельным sector-тестом handoff'а.

## Не входит в 5.3 (отложено)
- Miner (`up_drill`, астероиды) и Passenger — задачи 5.4/5.5.
- Закупка ракет/дронов (`BUYING_AMMO`), `sell_mode`/таймер 24ч,
  `fab_mining_requests`, выбор поставщика по цене, вторичный радиус поиска.
- Respawn замены при гибели (`SPAWNING`) — пока погибший трейдер исчезает.
- Формальная стыковка (`ship.Docked`) — трейдер паркуется у станции
  (`Approach`), не докается; перенос груза не требует dock.
- Денежная экономика NPC (`npc_credits`) — логистика бесплатная.
- Múltiples трейдеров на фабрику тюнятся `TradersPerFactory`, но баланс
  числа/частоты — забота фазы 7.

## Критерии (проверяется тестами)
- Трейдер автономно курсирует home↔dest, груз на фабрике меняется —
  unit-тесты контроллера + sector end-to-end (Haul вызван load→unload).
- Контроллер переживает прыжок через ворота (handoff) — sector-тест.
- Не атакует игроков (никогда не выдаёт `Attack`) — следует из чистоты
  контроллера (нет ветки Attack).

## Ссылки
- Старые файлы: `includes/npc_fab_ships.php`, `docs/npc_traders.md`.
- CLAUDE.md старого проекта: «Race AI, f_ships и перехват NPC-кораблей»,
  «NPC-автопилот через ворота».
