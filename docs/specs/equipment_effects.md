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
| `up_accumulator`   | MaxEnergy                      | +0.25            |
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

### Энергомодель (phase 10.3.1)

`ct_updates_energy` из оригинала не портируется по величинам — `energy_use_type`
(`always`/`hold`/`reverse`/`action`) и `energy_usage` берутся из
`configs/equipment.yaml`, магнитуды — баланс-решение Go-версии. Модель:

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
  списывает. Дрон/бур/прыжок проводятся тем же паттерном по мере появления
  механики.

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
