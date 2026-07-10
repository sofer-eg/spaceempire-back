# CaptureShipCommand — захват чужого корабля (TASK-100.3.9.4)

Оплот ветки захвата зонтичной TASK-100.3.9. Порт SP `DoCapture`
(`starwind/sql/db.sql:10560`): с модулем `up_capture` игрок захватывает чужой
корабль со сменой владельца. Строится **поверх**:
- `changeShipOwner` (TASK-100.3.9.2) — примитив смены владельца + выброс экипажа;
- knockoff-маркера `ShieldGeneratorDestroyed` (TASK-100.3.9.1) — единственный
  способ обнулить `MaxShield` цели и открыть её к захвату.

ЧТЗ — doc-4 §3 «Механика C» (FR-C1…C5), §5.1 (числа), §6 AC-4/5/6/7.

## Команда (`internal/sector/command.go`, `internal/sector/capture.go`)

`CaptureShipCommand{PlayerID, ShipID (атакующий), Target EntityRef (корабль-цель),
EnergyCost, Reply chan<- CaptureResult}`. Ответ `CaptureResult{Err, Captured bool}`.
Эталон — `AttackCommand` / `HackStationCommand` (тот же паттерн команда→worker).

### Гейты (каскад, порядок — FR-C2)

1. атакующий существует (`ErrShipNotFound`);
2. принадлежит игроку — `ship.PlayerID != c.PlayerID` → `ErrForbidden`;
3. атакующий НЕ в доке — `ship.Docked != nil` → `ErrShipDocked`;
4. установлен `up_capture` — `shipEquipmentLevel(ship,"up_capture") < 1` →
   `ErrEquipmentRequired`;
5. цель — корабль (`EntityKindShip`), не self → иначе `ErrInvalidAttackTarget`;
6. цель жива и в этом секторе (`s.ships[...]`, `HP > 0`) → иначе
   `ErrInvalidAttackTarget`;
7. цель **не дружественна** — `w.shipsAreFriendly(ship, target)` → иначе
   (`RelationFriend`: свой клан / объявленный друг / self через `Get(a,a)`)
   `ErrInvalidAttackTarget`. Damage-parity: тот же гейт, что урон (`!shipsAreFriendly`,
   `combat.go`) — захватываем любой не-союзный корабль (см. «Гейт враждебности»);
8. дистанция `≤ capture_range` (50) → иначе `ErrCaptureOutOfRange`;
9. **у цели НЕТ работающего щита** — `target.MaxShield > 0` → `ErrCaptureShielded`
   («захват при работающем щите невозможен»), см. ниже;
10. `Energy ≥ EnergyCost` → иначе `ErrNotEnoughEnergy`.

Все гейты пройдены → списать `up_capture.energy_usage` (100) **в любом исходе**
(FR-C3/C5), затем розыгрыш.

### Гейт щита — ключевое решение

Оригинал `DoCapture` (db.sql:10616): если у цели ЕСТЬ модуль `up_shield` (генератор
щита) → «захват невозможен». В StarWind `up_shield` = генератор; его отсутствие
(после knockoff `DestroyModule`) = захватываемо.

В spaceempire `up_shield` — БУСТ, а базовый щит даёт класс корабля (TASK-100.3.9.1
отверг глобальный гейт «нет up_shield → Shield=0»; вместо него durable-маркер
`domain.Ship.ShieldGeneratorDestroyed`, который при выбивании `up_shield` форсит
`MaxShield=0`). **Faithful-эквивалент: гейт по `target.MaxShield <= 0`** — нет
работающего генератора. Покрывает и «генератор сбит knockoff'ом» (маркер .1 →
MaxShield=0), и класс без щита; корабль с ЖИВЫМ щитом (класс-база ИЛИ up_shield-буст,
`MaxShield > 0`) — НЕ захватываем, даже если `Shield` временно просел до 0 в бою
(мгновенная просадка не открывает захват — регенерирует обратно). НЕ гейтим по
`Shield <= 0` и НЕ по «нет модуля `up_shield`» (класс-щитованные без up_shield иначе
стали бы захватываемы — неверно). `MaxShield <= 0` шире и робустнее маркера.

Сквозная связь с .1: `fireLasers` бьёт цель → при `Shield ≤ 20 %` knockoff может
выбить `up_shield` → `ShieldGeneratorDestroyed = true` → `knock.go` форсит
`MaxShield = 0` → этот гейт проходит → захват возможен.

## Розыгрыш (FR-C3)

`roll := w.rng.Float64() * 1000` (тот же инъектируемый `combat.RNG`, что knockoff
в .1 — детерминизм тестов). Успех, если `roll > capture_chance` (819, ~18 %); для
цели расы Кха'ак (`Race == 8`) порог `khaak_capture_chance` (876, ~12 %). Guard
оригинала «сектор 215» опущен (ЧТЗ C-04) — остаётся только пониженный khaak-порог.

## Успех (FR-C4)

1. `capturedRace := target.Race` (читаем ДО смены владельца — `changeShipOwner`
   обнулит `Race`; нужно для штрафа репутации).
2. `w.changeShipOwner(ctx, s, target, c.PlayerID)` (.2): owner + `Race=0` + сброс
   боёвки + выброс экипажа (`ShipCapturedEvent` на `ShipCapturedTopic`) + persist.
3. Репутация: `w.reputation.OnShipCaptured(ctx, capturer, capturedRace)` — **новый
   метод интерфейса `ReputationAwarder`** (по образцу `OnShipKilled`): +war
   атакующему + штраф race-standing по захваченной расе (только главные расы 1–5,
   как police-штраф; NPC/0 скип).
4. Журнал ОБОИМ через per-player bus-топик `ShipCaptureTopic(player)` +
   `ShipCaptureEvent{Captor, Success}`: атакующему `{Captor:true, Success:true}`
   («Корабль захвачен»), старому владельцу (если реальный игрок) `{Captor:false,
   Success:true}` («Ваш корабль захвачен»). WS-фрейм `ship_capture` (образец
   `station_hacked`); SPA рендерит (TASK-100.3.9.5). Отдельный event — НЕ дублирует
   `ShipCapturedEvent` из .2 (тот на глобальном топике для eject; журнал — на
   per-player, как knock/hack).

## Провал (FR-C5)

`hullDown := target.MaxHP / 16` (оригинал `maxHull >> 4`). Если `hullDown ≥
target.HP` → цель уничтожается штатным `killShip` (loot/respawn/entity_killed);
иначе `target.HP -= hullDown` + `immediateSave` (HP пишется `saveSQL`). Энергия
списана в любом исходе. Перед `killShip` — `target.LastAttacker = c.PlayerID`
(атрибуция kill-награды капитану). Журнал атакующему `{Captor:true, Success:false}`
(«Захват не удался»).

## HTTP (`internal/api/capture.go`)

`POST /api/cmd/capture` (тело `CaptureRequest{shipID, targetRef}`). Маппинг:
`ErrShipNotFound`→404, `ErrForbidden`→403, `ErrShipDocked`→400,
`ErrEquipmentRequired`/`ErrCaptureOutOfRange`/`ErrCaptureShielded`/`ErrNotEnoughEnergy`→422,
`ErrInvalidAttackTarget`→400. Энергия `up_capture.energy_usage` резолвится из
каталога один раз (`captureActionEnergyCost`, как `hackEnergyCost`).

## Config (`configs/capture.yaml` → `balance.CaptureConfig` → `sector.Config`)

`capture_chance` (819), `khaak_capture_chance` (876), `capture_range` (50) — НЕ
захардкожены (NFR-004). Проведены в `sector.Config.{CaptureChance,
KhaakCaptureChance, CaptureRange}` (defaults там же), маппинг в `app.go` рядом с
`Knock`/`HackRange`.

## Гейт враждебности — damage-parity (решение заказчика)

Гейт «цель не дружественна» = `!w.shipsAreFriendly(ship, target)` — **точный
паритет с гейтом урона** (`shipsAreFriendly`, `combat.go`), которым сбивают щит.
Захватываем любой НЕ-союзный корабль с обитым щитом: NPC (все фракции делят один
`__npc__`-owner → player/clan-оракул отдаёт `Neutral`, не `Friend` → захватываемы) и
нейтральных игроков; отклоняются только союзники (`RelationFriend`: свой клан /
объявленный друг / self).

Почему НЕ `IsHostile` (объявленная война): первая реализация гейтила по
`w.relations.IsHostile` (relation ≥ Hostile), но урон бьёт по `!shipsAreFriendly`,
а NPC делят `__npc__`-owner → `IsHostile(player, __npc__)=false` → NPC были
незахватываемы, хотя щит им сбить можно. Это ломало ОСНОВНОЙ кейс ЧТЗ C-06 (захват
NPC). Расовая враждебность NPC (матрица 8.13) живёт вне player/clan-оракула
(`raceMatrixTargeter` для AI, `targeter.go`), поэтому «правильная» враждебность для
player-команды недостижима через `relations`. Damage-parity восстанавливает C-06,
**faithful оригиналу** (`DoCapture` гейта враждебности не имел вовсе — только «не
свой корабль»), и согласуется с решением по hack (.3): фича должна реально работать.
Решение заказчика (AskUserQuestion, 2026-07-10).
