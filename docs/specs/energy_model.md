# Energy model — laser fire duration (TASK-100.3.25)

Энергомодель корабля: как пул энергии, восстановление и расход лазера дают
управляемую **длительность непрерывного огня**, откалиброванную по классам.
Расширяет `equipment_effects.md` (там — общая механика `EnergyDelta`/`ChargeEnergy`).

## Проблема, которую решает

`ct_updates` из StarWind никогда не калибровал энергию: у всех модулей
`energy_usage = 100` (DEFAULT). При плоском пуле `MaxEnergy = 100` и
`EnergyRecharge = 2/тик` базовый набор из двух `always`-модулей (`up_shield`,
`up_pro`) давал `EnergyDelta = −200` → энергия залипала на 0 → лазер (гейтит
энергию в `combat/laser.go`) вообще не стрелял. Ни один свежий корабль не мог
вести огонь.

## Механика

Лазер — **энергозависимое оружие**. За тик воркер (`sector/worker.go`) сначала
заряжает энергию (`chargeEnergies` → `combat.ChargeEnergy`), потом стреляет
(`fireLasers` → `combat.FireLaser`):

```
energy += EnergyRecharge + EnergyDelta      (клампится в [0, MaxEnergy])
если стреляем и energy >= LaserEnergyCost:   energy -= LaserEnergyCost
```

Обозначения:

- **P** = `MaxEnergy` — пул (per-class, см. таблицу);
- **R** = `EnergyRecharge` — восстановление/тик (per-class);
- **D** = `Σ always.energy_usage` — постоянный дренаж набора (`EnergyDelta = Σreverse − D`; у faithful-набора генераторов нет → `EnergyDelta = −D`);
- **L** = `LaserEnergyCost` = 5 (`StartLaserECost`, не меняется).

**Длительность непрерывного огня** (пул полон → троттл, когда `energy < L`):

```
T = P / (L − (R − D))          [тиков]
```

Огонь убывает энергию на `L − (R − D) = L − R + D` за тик. Калибровка держит
`L − R + D ≥ 1` (лазер всегда троттлится) и `R − D > 0` (в покое энергия
восстанавливается — **корабль никогда не залипает на 0** от базового набора).
Время восстановления из нуля ≈ `P / (R − D)` тиков.

## Калибровка каталога (`convert-equipment`)

Калибровочная таблица в `cmd/starwind-tools/convert-equipment` (`calibrate`)
переписывает энергополя `ct_updates`, `equipment.yaml` остаётся авто-генерён:

| Что | Было (DEFAULT) | Стало | Зачем |
|-----|----------------|-------|-------|
| `always` `energy_usage` (up_shield/up_pro/up_turret_control/up_hide/…) | 100 | **2** | базовый дренаж набора несколько очков/тик, не −100 за модуль |
| `reverse` `energy_usage` (up_generator) | 100 | **6** | один генератор перекрывает базовый дренаж и удлиняет огонь |
| `up_accumulator` `max_level` | 1 | **3** | пул можно удвоить трижды |

### Стоимости действий (`action`-цены)

Четыре `action`-модуля гейтятся живым пер-класс пулом (`command.go:465`
для запуска, `mining.go`/`transport.go` для бура/транспортера:
`if ship.Energy < cost → reject`, `Energy ≤ MaxEnergy`). При DEFAULT-100 класс
с пулом меньше 100 **не мог выполнить действие вообще** — свежий Разведчик
(пул 40) с 5 стартовыми ракетами и `up_launcher` L2 никогда не выпускал ракету
(`40 < 100`). Калибровка опускает эти цены ниже наименьшего пула (40):

| Модуль | Было | Стало | Тип списания |
|--------|------|-------|--------------|
| `up_launcher` (ракета) | 100 | **15** | разовое (за пуск) |
| `up_torpedo_launcher` (торпеда) | 100 | **20** | разовое (за пуск) |
| `up_drill` (бур) | 100 | **5** | **sustained** — списывается **каждый тик** бурения |
| `up_transporter` (транспортер) | 50 | **8** | разовое (за телепорт груза) |

Все четыре < 40 (наименьший пул) → **любое действие доступно любому классу**.
`up_drill` — единственный sustained: TS (cls9, пул 50, R6, набор D=4) при
бурении тратит `R − D − 5 = 6 − 4 − 5 = −3`/тик → бурит ~17 тиков непрерывно,
потом ждёт восстановления. Ракета/торпеда/транспортер — разовые.

Лазер (`StartLaserECost = 5`, не строка каталога) и прочие `action`-типы
(`up_engine`/`up_weapon_control`/`up_scanner`/`up_capture`/`up_hack`/
`up_antijump`/`up_drone_control`/`up_exdocking`/`up_jump_drive`) НЕ трогаются —
их реальные потребители пулом не гейтятся либо вне scope. Пассивные `hold`-модули
тоже не трогаются. `action`-цены **не входят** в `EnergyDelta` (его формируют
только `always`/`reverse`), поэтому калибровка длительности огня (ниже) не
меняется. В наборе дренаж дают только `up_shield`/`up_pro`, у крупных ещё
`up_turret_control`.

## Per-class пул/recharge (`convert-ship-classes`)

`ct_ship_classes` не имеет энергоколонок — пул/recharge задаёт калибровочная
таблица `energyCalibration` в `cmd/starwind-tools/convert-ship-classes`, ключ —
номер класса (`class`, 1..9). Race-100 спецкорабли переиспользуют номер класса и
наследуют его энергию. Загрузчик кладёт значения в `balance.ShipClass.MaxEnergy`
/`EnergyRecharge`; `baseShipStats` (`internal/app/spawner.go`) берёт их (0 у
classless → плоский `StartEnergy`/`StartEnergyChrg` fallback для скафандра/NPC).

| Класс | Модель (Argon) | D (always×2) | P / R | T огня, тиков (цель ±15%) | Восст. из 0, тиков |
|-------|----------------|--------------|-------|---------------------------|--------------------|
| 1 M1 Носитель | Колосс | 6 | 100 / 10 | 96 (100) | 25 |
| 2 M2 Эсминец | Титан | 6 | 100 / 10 | 96 (100) | 25 |
| 3 M3 Тяж. истр. | Нова | 4 | 70 / 6 | 22 (25) | 35 |
| 4 M4 Истр. | Охотник | 4 | 50 / 6 | 16 (15) | 25 |
| 5 M5 Разведчик | Разведчик | 4 | 40 / 5 | 9 (10) | 40 |
| 6 M6 Корвет | Кентавр | 6 | 95 / 9 | 46 (50) | 32 |
| 7 TL Супертр. | Мамонт | 6 | 120 / 9 | 58 (60) | 40 |
| 8 XX Спец. | Искатель | 6 | 120 / 9 | 58 (60) | 40 |
| 9 TS Транспорт | Меркурий | 4 | 50 / 6 | 16 (15) | 25 |

D = 4 у классов с набором `up_shield + up_pro` (M3/M4/M5/TS), D = 6 у тех, где
ещё `up_turret_control` (M1/M2/M6/TL/спец). Все T в пределах ±15% от цели.

## Апгрейды энергии (`equipment_effects.go`)

- **`up_accumulator`** — ×2 пул за уровень: `MaxEnergy += base×(2^level − 1)`,
  т.е. уровень L → пул ×2^L. Удваивает длительность огня. Разведчик:
  10 → 20 → 40 → 80 тиков (симтест: 9 → 19 → 39 → 79). `max_level = 3`.
- **`up_generator`** — `EnergyRecharge += base×0.25×level` (ускоряет откат в
  покое) + `reverse`-feed 6/тик в `EnergyDelta` (тянет больше always-модулей /
  удлиняет огонь). Не в базовом наборе — докупается игроком.

Faithful-набор (`ship_base_loadout.yaml`) генераторов/аккумуляторов НЕ содержит
(AC #5): игрок докупает их сам для удлинения огня.

## Симулятор и тест (AC #14)

`combat.SimulateFireTicks(pool, recharge, energyDelta, laserCost)` и
`combat.SimulateIdleRecoverTicks(pool, recharge, energyDelta)` (файл
`internal/combat/energy_sim.go`) — чистые функции, повторяющие поарифметике
рантайм `ChargeEnergy`+`FireLaser`. Тест `internal/combat/energy_sim_test.go`
грузит реальные конфиги и проверяет: per-class длительность ≈ цель (±15%),
удвоение от аккумулятора (scout 10→20→40→80), и idle-восстановление без 0-lock
для всех 9 классов.

`SimulateFireTicks` при устойчивом огне (`net = recharge + energyDelta ≥
laserCost` — напр. фит с генератором, который перекрывает лазер) возвращает
сентинел `combat.SustainedFire` (−1, «неограниченно»), а не зацикливается.
Базовый набор (без генератора) даёт `net < 0` и до сентинела не доходит.

## Затронутые артефакты

- `configs/equipment.yaml` — `always`→2, `reverse`→6, `up_accumulator` max_level→3;
  `action`-цены гейтимых модулей: launcher 15, torpedo 20, drill 5, transporter 8.
- `configs/ship_classes.yaml` — `max_energy`/`energy_recharge` per-class.
- `migrations/0057_ship_base_loadout_backfill.sql` — backfill стат-колонок
  (`max_energy`/`energy`/`energy_recharge`/`energy_delta`) per-class для
  существующих кораблей.
