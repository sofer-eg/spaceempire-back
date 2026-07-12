# Equipment effects (phase 10.14)

Спека эффектов оборудования (`ct_updates` / `up_*`) на ТТХ корабля при
портировании StarWind → spaceempire. Сопровождает задачу
`starwind/docs/tasks/phase10-14-shipyard-buy-ship-equipment.md`.

## Что делает оригинал StarWind

Источник: `starwind/sql/db.sql` (`ct_updates`, `ct_updates_energy`, SP `TO_*`).

Ключевой факт: **оригинал НЕ масштабирует характеристики корабля формулой
от уровня апгрейда.** В `ct_updates` нет числовых колонок дельт статов —
только `price`, `price_per_level`, `max_level`, `dependance`,
`energy_use_type`, `energy_usage`, пороги ранга. ТТХ корабля целиком берутся
из класса (`ct_ship_classes`: speed, shield, hull, shield_charge, laser…), а
апгрейды работают в трёх плоскостях:

1. **Capability-флаги** — наличие модуля в `updates` включает возможность
   (бур добывает руду, сканер расширяет обзор, jump-drive прыгает, hide даёт
   стелс, launcher пускает ракеты, autopilot/docking и т.п.).
2. **Energy-режимы** (`ct_updates_energy`: `energy_mode`,
   `energy_mode_usage`, `energy_mode_effect`) — у части модулей есть режимы
   мощности 0..3 с коэффициентами потребления/эффекта; применяются в боевом
   цикле.
3. **Счётчики** — `up_drone_control` уровень = число управляемых дронов,
   `up_turret_control` — число активных турелей.

Базовые модули класса помечены `is_base=1` (предустановлены на NPC-кораблях
через `ct_npc_ship_modules`); остальные докупаются.

## Модель spaceempire (решение Go-версии)

Задача требует «полный маппинг 22 типов» + «установка реально меняет ТТХ».
Поскольку достоверной аддитивной формулы в оригинале нет, магнитуды —
**баланс-решение Go-версии**, а не порт величин. Принцип: модуль даёт
скромный аддитивный буст к базовому стату класса, скейл по уровню установки.

Хранение: `ships.Equipment []InstalledEquipment{EquipmentID, Type, Level}`
(JSONB-колонка `equipment`, миграция 0045). Источник истины статов — корабль
в RAM воркера; при установке/снятии стат пересчитывается
`effective = base(class) + Σ delta(equipment)` и персистится вместе со
списком.

### Стат-модули (меняют ТТХ)

`delta = round(base_field * coeff * level)` поверх базовых статов класса:

| Тип                | Поле ТТХ                       | coeff за уровень |
|--------------------|--------------------------------|------------------|
| `up_engine`        | MaxSpeed, Acceleration         | +0.08            |
| `up_shield`        | MaxShield                      | +0.15            |
| `up_shield`        | ShieldRecharge                 | +0.10            |
| `up_generator`     | EnergyRecharge                 | +0.25            |
| `up_accumulator`   | MaxEnergy (×2/уровень)         | +(2^level−1)     |
| `up_lb`            | LaserDamage                    | +0.10            |
| `up_pro`           | MaxShield (противоракетный)    | +0.10            |
| `up_weapon_control`| LaserDamage                    | +0.08            |
| `up_turret_control`| LaserDamage                    | +0.08            |

### Capability-модули (хранятся, ТТХ не меняют)

`up_launcher`, `up_torpedo_launcher`, `up_drill`, `up_scanner`,
`up_jump_drive`, `up_hide`, `up_autopilot`, `up_docking`, `up_exdocking`,
`up_antijump`, `up_capture`, `up_hack`, `up_drone_control`, `trade_up`.

Их эффект — разблокировка подсистемы. Проводка в геймплей делается, когда
подсистема существует и тогда читает `Ship.Equipment` (дроны 4.4 →
`up_drone_control` уровень = cap; ракеты 4.3 → `up_launcher`; радар 10.20 →
`up_scanner`/`up_hide`). Фабриковать им ТТХ-дельты было бы недостоверно, так
что в 10.14 они install-only.

### Энергомодель (phase 10.3.1; калибровка — TASK-100.3.25)

`ct_updates_energy` из оригинала не портируется по величинам — `energy_use_type`
(`always`/`hold`/`reverse`/`action`) и `energy_usage` берутся из
`configs/equipment.yaml`, магнитуды — баланс-решение Go-версии.

> **Калибровка длительности огня — `energy_model.md`.** `always`-дренаж и
> `reverse`-feed переписаны калибровочной таблицей `convert-equipment`
> (`always`→2, `reverse`→6), пул/recharge заданы per-class в
> `convert-ship-classes`, `up_accumulator` удваивает пул за уровень
> (`max_level 3`). Так лазер даёт управляемую длительность огня по классам, а
> корабль не залипает на 0 от базового набора. Формула `T = Pool/(L−(R−D))` и
> таблица per-class — в `energy_model.md`.

Модель:

- **Per-tick дельта.** `balance.Equipments.EnergyDelta(eq)` =
  `Σ reverse(energy_usage) − Σ always(energy_usage)`: `reverse`-модули
  (генераторы) пополняют пул, `always`-модули (постоянные потребители) его
  истощают. `hold`/`action` в стабильную дельту не входят. Не скейлится по
  уровню установки. Кэшируется на корабле (`domain.Ship.EnergyDelta`) при
  install/uninstall, как effective-статы, и персистится (колонка
  `ships.energy_delta`, миграция 0053).
- **Заряд за тик.** `combat.ChargeEnergy` двигает `Energy` на
  `EnergyRecharge + EnergyDelta`, клампит в `[0, MaxEnergy]`. Отрицательная
  нетто-ставка осушает пул до нуля.
- **Обесточивание модуля.** При `Energy<=0` capability-модуль `always` считается
  выключённым; конкретно стелс `up_hide` всплывает (`hideStealthed` в
  `sector/snapshot.go`). Прочие `always`/`hold`-модули получат свой гейт
  «выключен при нуле энергии», когда их подсистема будет проведена.
- **`action`-расход.** Разовое списание при действии. Пуск ракеты тратит
  `energy_usage` ленчера: HTTP-handler берёт цену из каталога
  (`launchActionEnergyCost`), кладёт в `LaunchMissileCommand.EnergyCost`, воркер
  при `Energy < cost` отклоняет пуск `ErrNotEnoughEnergy` (HTTP 422), иначе
  списывает. Бур проведён по тому же паттерну (см. ниже); дрон/прыжок — по мере
  появления механики.

### `up_drill` → игровая добыча руды (phase 10.3.6)

Игровая добыча — отдельный путь от NPC-шахтёров (`ai.Mine`/`applyMine` не
тронуты). Игрок включает sustained-режим командой `POST /api/cmd/mine`
(`MineCommand`): воркер ставит `domain.Ship.MiningTarget *AsteroidID` (RAM-only
intent, как `AttackTarget` — не персистится), сбрасывается при handoff (ворота),
death (корабль удаляется), depletion, `MoveCommand` (новый курс) и
`CeaseFireCommand`/`MineCommand{Asteroid:nil}` (стоп). Гейт старта: `up_drill`
установлен (`ErrEquipmentRequired` → 422), корабль не пристыкован, астероид жив и
в `cfg.MineRange`.

Каждый тик `tickPlayerMining` (после `tickAI`, до движения) для каждого корабля
с `MiningTarget`: гейт `up_drill`, удержание на станции (обнуляет
Target/Vel — как `applyMine`), проверка дистанции, **энерго-гейт** (`action`:
`cfg.MineEnergyCost` из каталога `up_drill.energy_usage`, при нехватке тик не
бурит, режим сохраняется), извлечение `cfg.MineRate` руды (баланс-решение
Go-версии: **5/тик**, в паритет с NPC `miner.DrillRate=5`; величины оригинала не
портируются), списание энергии, `ast.Mass -= rate`, `depleteAsteroid` + сброс
`MiningTarget` при опустошении.

**Депозит зависит от уровня** `shipEquipmentLevel(ship,"up_drill")`:
- **ур.1** → руда выпадает контейнером в космос рядом с астероидом
  (`ContainerRepo.SpawnContainer` — та же машинерия лута, что и kill-drop;
  подбирается стандартным pickup-container);
- **ур.2** → прямо в трюм (`MinerLogistics.AddOre`, как NPC); при полном трюме
  (`cargo.ErrNoSpace`) — fallback в контейнер. Прочие ошибки депозита оставляют
  астероид нетронутым и ретраят на следующем тике.

### `up_jump_drive` → бесшовный прыжок без ворот (phase 10.3.7)

Порт SP `DoJump` **режим 0** (прыжок в любой выбранный игроком сектор). Команда
`POST /api/cmd/jump-drive {shipID, targetSectorID}` → `JumpDriveCommand`. Воркер,
владеющий текущим сектором корабля, валидирует и переиспользует `executeJump`
(та же машинерия, что и прыжок через ворота — persist в целевой сектор + publish
`JumpEvent` в `sector.<target>.intake` + eviction из RAM источника). Прибытие —
случайная точка в `±1600` у центра целевого сектора (faithful `pos = 1600 -
rand()*3200`; центр берётся из `Sector.Bounds`, для origin-центрированных
секторов = `(0,0)`). Гейты (в порядке проверки):

1. корабль есть в секторе и принадлежит игроку (`ErrForbidden`);
2. воркер сконфигурирован для handoff (topology+bus, иначе `ErrHandoffUnavailable`);
3. **не пристыкован** — `Docked==nil`, иначе `ErrShipDocked` (HTTP 409). Осознанное
   сужение MVP: undock-через-прыжок трогал бы docking-internals (занятость
   станции/ангара) — вне scope; ценность прыжка в бегстве из открытого космоса.
   Оригинал (mode 0) отстыковывал inline (`location=0`) — вынесено в follow-up;
4. установлен `up_jump_drive` (`shipEquipmentLevel>=1`), иначе `ErrEquipmentRequired`
   (HTTP 422);
5. **исправный генератор щита** — `MaxShield>0`, иначе `ErrShieldRequired` (422).
   `MaxShield==0` = нет `up_shield` либо генератор сбит в бою
   (`ShieldGeneratorDestroyed`);
6. **кулдаун** не истёк, иначе `ErrJumpOnCooldown` (HTTP 429);
7. текущий сектор не в `Config.JumpDriveForbiddenSectors`, иначе
   `ErrJumpForbiddenSector` (400);
8. целевой сектор существует в топологии и не равен текущему, иначе
   `ErrInvalidSector` (400). Режим 0 = любой существующий сектор, без hop-лимита.
9. **нет глушения `up_antijump`** — в том же секторе нет ДРУГОГО запитанного
   корабля с `up_antijump` в радиусе `Config.AntijumpRange`, иначе
   `ErrJumpBlockedByAntijump` (HTTP 409). См. раздел ниже.

**Стоимость — faithful (Развилка 1=B):** плата = обнуление щита (`Shield=0`) +
требование исправного генератора (см. гейт 5). **Энергия НЕ списывается.** Хотя
каталог помечает модуль `action/100`, для прыжка энергия не проводится — это
осознанное решение: faithful-стоимость оригинала это только щит + кулдаун, а
`energy_usage` в `ct_updates` для jump-drive не участвовал в `DoJump`. Каталог не
трогаем.

**Кулдаун — faithful real-time (Развилка 2=A):** реальное время по уровню модуля
через wall-clock `domain.Ship.LastJumpAt` (`time.Time`): **level 1 = 60 мин,
level 2 = 30 мин** (`up_jump_drive` `max_level=2`). Длительности — в
`sector.Config` (`JumpDriveCooldownL1/L2`, дефолты 60/30 мин; тесты подставляют
крошечные значения). Проверка `w.clock.Now().Sub(LastJumpAt) < cd` (нулевой
`LastJumpAt` = ни разу не прыгал → кулдауна нет). При успехе стамп обновляется на
`w.clock.Now()`. Поле **персистится** (`ships.last_jump_at TIMESTAMPTZ NULL`,
миграция 0059; NULL при нуле) и **переживает handoff** — `executeJump` копирует
`*ship` (Shield=0 и LastJumpAt в клоне `relocated`), так что кулдаун цел и после
рестарта (`LoadAll`), и после смены сектора.

**Запретные сектора** (`Config.JumpDriveForbiddenSectors`, дефолт пусто): порт
`object_sector = 215 or 203` из оригинала — блок прыжка ИЗ сектора. StarWind-
специфичные 215/203 **не хардкодятся**; deployment задаёт свой список.

**Follow-up (вне scope, отдельные задачи):** маяки (`DoJump` mode 2 + ACL
`jump_beacon_acl`); авто-возврат домой (mode 1 + `home_werf`/автопилот
task-100.3.11); undock-через-прыжок; анти-джамп дрон class 7 (в оригинале
`DoJump` глушил прыжок ещё и дроном class 7 — в spaceempire такой сущности дрона
нет, отдельная задача).

### `up_antijump` → блок прыжка соседей (TASK-100.3.8)

Порт гипер-помех из SP `DoJump` (`starwind/sql/db.sql:12103-12139`): прыжок
глушится, если рядом есть запитанный `up_antijump`. Проверка `w.antijumpActive`
в `JumpDriveCommand.apply` **до оплаты щита/кулдауна** (гейт 9 выше), поэтому
заблокированный прыжок ничего не стоит (щит и `LastJumpAt` не трогаются).

- **Кого блокирует:** ЛЮБОЙ owned-корабль с установленным `up_antijump`
  (`level>=1`) и `Energy>0` в радиусе — faithful SP (в оригинале
  `object_owner != 0`, БЕЗ фильтра враждебности). Сам прыгун исключён из скана
  (свой `up_antijump` свой прыжок не глушит).
- **Активация — энергоапкип:** модуль `always`/15/тик (`equipment.yaml`); зона
  активна, только пока носитель `Energy>0` (паттерн «Energy==0 = без питания»,
  как у стелса). Отдельного тумблера/WS-состояния нет. Детали энергобаланса — в
  `energy_model.md`.
- **Радиус:** `Config.AntijumpRange` (дефолт **640**), круговой Euclidean
  (`Pos.Sub().Length()`). Оригинал тестировал коробку `|dx|<640 && |dy|<640` —
  порт как круг для единообразия с `MineRange`/`TransporterRange`/`CaptureRange`.
- **Уровень модуля** (`max_level=2`) механически на радиус/блок не влияет —
  faithful (SP читал только факт `up_on`, не уровень).

**Фронт-UI (реализовано, TASK-129):** кнопка «⚡ Прыжок» в HUD корабля
(`CombatHUD`, гейт по `up_jump_drive`, disabled без исправного генератора щита)
ведёт на карту галактики (`/galaxy`) в режиме прыжка; клик по сектору →
`POST /api/cmd/jump-drive`, на успехе возврат на `/sector`. Русский маппинг
ошибок бэка — чистая функция `jumpDriveErrorText` (front `api.ts`, покрыта
unit-тестом).

## Валидация установки

Проверяется (данные есть в каталоге):

- **класс**: выбранная строка каталога должна совпадать по `ShipClass` с
  классом корабля (`balance.ShipClass.Class`) — это и тариф цены, и
  применимость; `ShipClass==0` — универсальная строка, годится всем;
- **раса**: `Equipment.Race==0` (универсальная) или == расе корабля;
- **ранг**: репутация игрока (`war_rate`/`trade_rate`/`race_rate`) должна
  быть не ниже порогов `min_war_rate`/`min_trade_rate`/`min_race_rate`
  строки каталога; недобор хотя бы по одной оси → `ErrRankTooLow` (HTTP 422);
- **уровень**: `1 <= level <= max_level` (для `max_level==0` допустим
  только level 1);
- **зависимость**: если `Dependance != "none"/""`, модуль такого `Type`
  должен быть уже установлен (энергоцепочка
  `up_generator → up_accumulator → остальные`);
- **слот**: один модуль на `Type` (повторная установка того же типа
  отклоняется — сначала снять).

**Ранг** (10.3.4): модель репутации игрока появилась в 10.3.3
(`players.war_rate`/`trade_rate`/`race_rate`). Handler читает её через
`players.GetReputation` и передаёт в `ResolveInstall` как
`balance.Reputation`; каталожный слой остаётся без зависимости от
persistence. Сравнение по принципу «не ниже порога» (равенство проходит);
пороги `==0` ставятся при любой репутации (включая дефолтные нули).

## Цена

`price + level * price_per_level` (как в оригинале) списывается с кошелька
игрока в одной транзакции с записью оборудования. Снятие — без возврата
средств (MVP).
