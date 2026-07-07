# Базовая комплектация корабля при спавне (TASK-100.3.25)

Спека порта таблицы `ct_npc_ship_modules` из StarWind в конфиг базовой
комплектации spaceempire. Свежий корабль игрока раньше спавнился с
`ships.equipment = '[]'` — из-за этого вкладка «Верфь → Дооснащение» для
стартового Разведчика была стеной disabled-кнопок (почти всё оборудование
гейтится зависимостью `up_accumulator` → `up_generator`). Теперь корабль
спавнится/покупается с базовым набором своей модели.

## Что делает оригинал StarWind

Источник: `starwind/sql/db.sql`, SP `CreateStandartPilotShip` (~9631) и таблица
`ct_npc_ship_modules` (строка 656, 392 строки, 72 корабля race 1‑8 × type 1‑9).

`CreateStandartPilotShip` пишет базовый набор напрямую из `ct_npc_ship_modules`
(`INSERT ... SELECT`), **в обход** проверки `dependance` и порогов ранга. Схема
таблицы: `(ID, race, type, module_type, module_level)`.

Ключевой принцип: `dependance` (цепочка `…→up_accumulator→up_generator→none`)
— это **только UI‑гейт покупки на верфи**. При спавне он не применяется, поэтому
`up_launcher`/`up_pro` стоят на корабле без аккумулятора, и это штатно. Порт
воспроизводит это поведение: базовый набор строится напрямую (не через
`ResolveInstall`), минуя dependance и rank; интерактивная установка на верфи
гейты сохраняет.

Модули базового набора: `up_engine`, `up_shield`, `up_weapon_control`,
`up_turret_control`, `up_launcher`, `up_pro`. **`up_generator`/`up_accumulator`
не входят ни в один набор** — их игрок докупает сам.

## Маппинг ct_npc_ship_modules → конфиг

Конвертер `cmd/starwind-tools/convert-ship-loadout` резолвит каждую строку
`(race, type, module_type, module_level)` в элемент `{equipment_id, type, level}`:

1. **Класс корабля** берётся из `ship_classes.yaml` по ключу `(race, type)`
   (уникален по всему каталогу из 86 моделей).
2. **EquipmentID** ищется в `equipment.yaml`: единственный ряд с
   `type == module_type` и `class == shipClass`, с предпочтением
   race‑specific (`race == shipRace`) над универсальным (`race == 0`). Все 392
   строки резолвятся однозначно (0 unresolved, 0 ambiguous). Среди 6 базовых
   типов race‑specific рядов нет — предпочтение оставлено для точности.
3. **Кламп уровня** к `max_level` каталога (см. ниже).

Набор по типам корабля (класс = `ct_ship_classes.class`):

| type | класс | up_engine | up_shield | up_weapon_control | up_turret_control | up_launcher | up_pro |
|------|-------|-----------|-----------|-------------------|-------------------|-------------|--------|
| 1 | 1 (M1) | 1 | 1 | 1 | 3 | 5 | 5 |
| 2 | 2 (M2) | 1 | 1 | 1 | 3 | 5 | 5 |
| 3 | 3 (M3) | 1 | 1 | 1 | — | 5 | 5 |
| 4 | 4 (M4) | 1 | 1 | 1 | — | 4 | 4 |
| 5 | 5 (M5) | 1 | 1 | 1 | — | 2 | 4 |
| 6 | 6 (M6) | 1 | 1 | 1 | 2 | 5 | 5 |
| 7 | 7 (TL) | 1 | 1 | 1 | 1 | 5 | **4** (клампнут с 5) |
| 8 | 8 (XX) | 1 | 1 | 1 | 3 | 5 | 5 |
| 9 | 9 (TS) | 1 | 1 | — | — | 5 | 2 |

Это относится к race 1‑8 (одинаковый шаблон). Исключения:

- **race 8 / type 8** (Кластер Хааков): `up_turret_control` = **2** (не 3).
- **race 7 / type 8** (Ксенон ЛХ): в `ship_classes` это `class 3`, а не 8 —
  поэтому набор M3‑подобный (`up_launcher` id 3, `up_pro` id 12), а
  `up_turret_control` резолвится в class‑3 ряд id 115 с `max_level 0` →
  **клампнут с 3 до 1**.

Стартовый Argon‑разведчик (**race 1 / type 5**, id 77):
`up_engine 1 (62) · up_shield 1 (42) · up_weapon_control 1 (71) · up_launcher 2 (5) · up_pro 4 (14)`.

### Клампы уровня (9 строк)

Оригинал вставлял уровни в обход cap каталога, поэтому 9 строк превышают
`max_level` и клампятся:

- **up_pro L5 → L4** (eqid 16, `max_level 4`) — для class 7 у всех рас
  (race 1‑8, type 7): 8 кораблей.
- **up_turret_control L3 → L1** (eqid 115, `max_level 0` → трактуется как 1) —
  race 7 / type 8.

AC #5 (race 1 / type 5) клампы не затрагивают.

### Спецмодели без набора (14)

`race 100` (type 10‑21: Гиперион и уникальные) и `race 3 / 8 type 10` не имеют
строк в `ct_npc_ship_modules` → спавнятся с пустым набором (faithful оригиналу).
Конфиг покрывает их пустым `modules: []`.

## Сворачивание эффектов в статы (обязательно)

Sector‑worker и cold‑start **не пересчитывают** эффекты из `equipment` — стат‑
колонки (`max_shield`, `max_energy`, `energy_delta`, `max_speed`, `acceleration`,
`shield_recharge`, `energy_recharge`, `laser_damage`, `turn_rate`, `cargobay`,
`radar_range`) авторитетны; `balance.ApplyEquipmentEffects` вызывается только в
`shipyard_outfit.go` (install/uninstall). Поэтому при спавне/покупке и в
backfill‑миграции набор **сворачивается** в стат‑поля так же, как это делает
outfit:

```
eff := balance.ApplyEquipmentEffects(baseShipStats(cls, cfg), loadout)
energyDelta := equipment.EnergyDelta(loadout)
```

и `eff.*` + `energyDelta` пишутся и в `ship`, и в стат‑колонки; `Shield =
MaxShield`, `Energy = MaxEnergy` (свежий корабль заряжен). Иначе JSONB и статы
разъедутся, и первый же uninstall дал бы скачок статов.

Базовые модули реально меняют статы (см. `equipment_effects.md`): `up_pro` L4 =
+40 % shield, `up_shield` L1 = +15 % shield / +10 % recharge, `up_engine` =
+8 % speed/accel, `up_weapon_control`/`up_turret_control` = +8 % laser.
`up_launcher` — capability, статы не трогает. `ship_classes.yaml` — «голые»
статы, модули сверху — faithful оригиналу (`CreateStandartPilotShip` тоже даёт
`max_shield = class base + модули`).

`EnergyDelta` для базового набора **отрицателен**: `up_shield`, `up_pro` и
`up_turret_control` имеют `energy_use_type = "always"`, т.е. постоянно
потребляют энергию (`EnergyDelta = −Σ always.energy_usage`). После калибровки
энергомодели (TASK-100.3.25) `always.energy_usage = 2`, поэтому базовый дренаж
набора мал: −4 (два `always`) или −6 (три). Пул/recharge заданы per-class, так
что лазер даёт управляемую длительность огня и корабль не залипает на 0 — детали
в `energy_model.md`. (Раньше `energy_usage = 100` давало −200/−300 и
обесточивало лазер.)

`energy_delta` теперь пишется и в `ships.Create` (раньше только в
`SaveEquipment`), иначе cold‑start прочитал бы `DEFAULT 0` и рассинхронил
колонку с JSONB.

## Реализация (по слоям)

| Слой | Файл |
|------|------|
| Конвертер | `cmd/starwind-tools/convert-ship-loadout/main.go` |
| Конфиг | `configs/ship_base_loadout.yaml` (86 моделей, 72 непустых) |
| Balance‑лоадер | `internal/balance/ship_loadout.go`, `ship_loadout_loader.go` |
| Config‑путь | `internal/pkg/config/config.go` → `BalanceConfig.ShipLoadoutPath` |
| Загрузка/DI | `internal/app/app.go` (`LoadShipLoadoutsFromFile`, проброс в спавнер/outfit) |
| Спавн | `internal/app/spawner.go` (`buildStarterShip`) |
| Покупка | `internal/app/shipyard_outfit.go` (`buildPurchasedShip`) |
| Персист energy_delta | `internal/persistence/ships/repo.go` (`Create`) |
| Миграция | `migrations/0057_ship_base_loadout_backfill.sql` |
| Генератор миграции | `cmd/starwind-tools/gen-ship-loadout-backfill/main.go` |

### Миграция backfill

Дозаполняет уже существующие пустые корабли игроков (`equipment = '[]'`,
`is_spacesuit = false`, `player_id NOT IN (SELECT id FROM players WHERE login =
'__npc__')`) — по одному `UPDATE` на класс (72 непустых), с посчитанным в Go тем
же `ApplyEquipmentEffects`/`EnergyDelta` JSONB и стат‑колонками. NPC‑корабли
(системный игрок `__npc__`) и уже дооснащённые корабли не трогаются. Down —
возвращает `equipment = '[]'` игрокам (стат‑колонки не откатывает: их
до‑backfill значения нигде не сохранены, повторный Up пересчитывает их из класса).

Базовые статы генератор воспроизводит из `internal/app.baseShipStats` с
дефолтным `ShipSpawnerConfig{}.withDefaults()` (StartEnergy 100,
StartEnergyChrg 2, StartTurnRate π/4, StartLaserDamage 10, warshipLaserDivisor
10), который `app.go` всегда и использует. При изменении этих дефолтов или
каталогов генератор нужно перезапустить.

## Регенерация артефактов

```bash
cd back
go run ./cmd/starwind-tools/convert-ship-loadout \
    -sql ../../starwind/sql/db.sql \
    -classes configs/ship_classes.yaml \
    -equipment configs/equipment.yaml \
    -out configs/ship_base_loadout.yaml

go run ./cmd/starwind-tools/gen-ship-loadout-backfill \
    -classes configs/ship_classes.yaml \
    -loadouts configs/ship_base_loadout.yaml \
    -equipment configs/equipment.yaml \
    -out migrations/0057_ship_base_loadout_backfill.sql
```
