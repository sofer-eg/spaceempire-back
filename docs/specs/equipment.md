# ct_updates → каталог оборудования кораблей (8.16)

Порт справочника дополнительного оборудования (апгрейдов) из оригинального
StarWind. Источник: `starwind/sql/db.sql` `ct_updates` (139 строк, 16 колонок,
22 типа). `ct_updates_energy` (per-mode энергокоэффициенты) — вне MVP.

## Конвертер

`cmd/starwind-tools/convert-equipment -sql <db.sql> -out configs/equipment.yaml`
читает `ct_updates` (дамп фактически UTF-8) и пишет `equipment:` секцию,
зеркало колонок: `id, type, description, max_level, race, class, price,
price_per_level, min_war_rate, min_trade_rate, min_race_rate, is_base,
position, dependance, energy_use_type, energy_usage`.

## Каталог

`balance.Equipment` (зеркало колонок; `is_base` int→bool, `class`→`ShipClass`).
Ключ каталога — **`ID`** (PK), потому что `type` не уникален: один тип
(напр. `up_engine`) повторяется по 9 ship-классам с разной ценой. UNIQUE-ключ
оригинала — `(type, race, class)`. Аксессоры: `GetEquipment(id)`,
`AllEquipment()`, `EquipmentByType(t)`, `EquipmentByShipClass(c)`,
`EquipmentCount()`. `LoadEquipmentFromFile` грузит из YAML. `domain.EquipmentID`.

## 22 типа и энергоцепочка

`up_launcher, up_pro, up_docking, up_hide, up_lb, up_shield, up_capture,
up_drone_control, trade_up, up_engine, up_weapon_control, up_autopilot,
up_generator, up_accumulator, up_drill, up_scanner, up_turret_control,
up_exdocking, up_hack, up_torpedo_launcher, up_jump_drive, up_antijump`.

Энергоцепочка: `up_generator` (`energy_use_type=reverse`, `dependance=none`)
производит энергию → `up_accumulator` (`hold`, `dependance=up_generator`)
хранит → большинство модулей зависят от `up_accumulator`. Поле `dependance`
= «при отключении этой зависимости модуль тоже выключается».

`position`: 1 — внутренний слот, 2 — внешний. `energy_use_type`:
`always`/`action`/`reverse`/`hold`.

## API / Frontend

`GET /api/equipment` отдаёт каталог. Фронт: `fetchEquipment()` + тип `Equipment`
(пока без UI — экран дооснащения на верфи отложен в follow-up).

## Отступления MVP (отложено)

- **Установка/снятие** оборудования на корабль (runtime, аналог таблицы
  `updates`): покупка на верфи, списание денег, слоты/зависимости/ранг.
- **Игровые эффекты** (апгрейд реально меняет статы): `up_engine`→скорость,
  `up_shield`→щит, `up_generator/up_accumulator`→энергия и т.д.
- **Энергомодель режимов** (`ct_updates_energy`, `energy_use_type`).
- **Расовые ограничения** (`up_hack` у пиратов, race=6) и пороги по рангу
  (`min_*_rate`) — данные сохранены в каталоге, логика не реализована.
- UI-экран оборудования.
