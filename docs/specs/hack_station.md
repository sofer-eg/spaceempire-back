# Взлом торговой станции (`up_hack` / SP `UseHack`)

Порт SP `UseHack` (`starwind/sql/db.sql:38768`) — TASK-100.3.9.3, ЧТЗ doc-4 §3
«Механика D» (FR-D1…D6), §5.3 sequence, §6 AC-8/9/10. Независимая ветка
зонтичной TASK-100.3.9 (НЕ связана с захватом кораблей `up_capture`).

`up_hack` в оригинале — **пиратский (race=6) модуль грабежа торговой станции**
(кража ~15% / порча ~5% товара), а не «взлом систем корабля». ЧТЗ фиксирует
фактическую механику; расовый гейт снимается, чтобы модуль был доступен игроку.

**Расширение scope (решение заказчика, 2026-07-10):** взлом бьёт **торговую
станцию (`EntityKindTradeStation`=4) ИЛИ производственную станцию
(`EntityKindStation`=2)**. Осознанное отступление от ЧТЗ C-07: на живых данных
товар держат производственные станции (заполнение ~47%, `max_stock` 10000), а
торговые — почти пустые resale-рынки (seed `stock 500 / max_stock 1e6`). Слой
рынка (`station_goods`) owner-generic: обе — валидные владельцы рынка
(`isStationKind` → owner_kind 2/4).

## Команда

`HackStationCommand` (`internal/sector/hack.go`): `PlayerID`, `ShipID` (взломщик),
`Target` (EntityRef торговой ИЛИ производственной станции), `EnergyCost`,
`Reply chan<- HackResult`.
HTTP-вход `POST /api/cmd/hack` (`internal/api/hack.go`), тело
`{shipID, targetRef}`; `PlayerID` — из сессии. Применяется воркером в начале
тика (one-writer), как `AttackCommand`/`LaunchMissileCommand`.

## Гейты (каскад, в порядке)

| # | Условие | Ошибка (HTTP) |
|---|---------|---------------|
| 1 | корабль-взломщик существует в секторе | `ErrShipNotFound` (404) |
| 2 | принадлежит игроку (`ship.PlayerID == PlayerID`) | `ErrForbidden` (403) |
| 3 | установлен `up_hack` (`shipEquipmentLevel >= 1`) | `ErrEquipmentRequired` (422) |
| 4 | `Target.Kind` ∈ {`EntityKindTradeStation`(4), `EntityKindStation`(2)} | `ErrInvalidAttackTarget` (400) |
| 5 | станция есть в `s.statics.TradeStations` (kind 4) / `s.statics.Stations` (kind 2) | `ErrInvalidAttackTarget` (400) |
| 6 | станция достроена (`Built`) | `ErrInvalidAttackTarget` (400) |
| 7 | раса станции ≠ 6 (пиратскую взломать нельзя) | `ErrInvalidAttackTarget` (400) |
| 8 | дистанция `ship.Pos → station.pos ≤ hack_range` (50) | `ErrHackOutOfRange` (422) |
| 9 | `ship.Energy >= EnergyCost` (проверка, без списания) | `ErrNotEnoughEnergy` (422) |
| 10 | товара на целевой позиции `>= hack_goods_min_fraction` (0.3) от `max_stock` | `ErrHackTooLittleGoods` (422) |

Резолв цели — `resolveHackStation(s, ref)`: диспетчеризация по `ref.Kind`
(TradeStation → `s.statics.TradeStations`, Station → `s.statics.Stations`),
возвращает нормализованный `hackTarget{pos, race, built}`. Гейт 10 проверяется
внутри `StationRobber.Rob` (нужен DB-read stock). Энергия
списывается **только** после успешного `Rob` (провал по гейту 10 не тратит
энергию, как «too little goods» в оригинале — эффект не начинается).

## Эффект (`StationRobber.Rob` → воркер)

Модель рынка: `station_goods (owner_kind, owner_id, goods_type_id, stock,
max_stock, buy_price, sell_price)`. И торговая станция (`owner_kind=4`), и
производственная (`owner_kind=2`) — валидные владельцы рынка
(`trade.Repo`/`isStationKind`). `trade.Service.Rob` принимает `EntityRef`
владельца и owner-generic (ничего не хардкодит про owner_kind).

**Целевой товар.** SP грабит один «главный» товар станции (`sg.type=1`). В
spaceempire у торговой станции нет единственного «производимого» товара —
рынок универсальный. Выбираем **позицию с наибольшим текущим `stock`**
(tie-break: наименьший `goods_type_id`) — «самая жирная куча», которую и берёт
пират. Гейт «≥30% максимума» применяется к `max_stock` этой позиции.

**Расчёт** (`trade.Service.Rob`, одна транзакция):
- `robbed = floor(hack_rob_fraction · stock)` (0.15),
- `damaged = floor(hack_damage_fraction · stock)` (0.05),
- клэмп как в SP: если `robbed+damaged > stock` → `robbed = 0`; если
  `damaged > stock` → `damaged = 0`,
- `AdjustStock(station, gtype, -(robbed+damaged))` — списываем украденное +
  испорченное со `stock` (испорченное просто уничтожается, награбленное — в лут).

**Лут** (faithful SP): при `Level(up_hack) >= 2` **и** есть место в трюме —
`robbed` кладётся в трюм взломщика (`AddCargo` в той же транзакции, что и
списание stock → атомарно; `Delivered=true`). Иначе (`Level 1` или трюм полон)
— воркер спавнит контейнер рядом со станцией с `robbed` единицами
(`ContainerRepo.SpawnContainer`, образец `spawnOreContainer`/mining).

**Репутация.** Штраф расе станции (только основные расы 1–5) пропорционально
доле изъятого: `penalty = round((robbed+damaged)/max_stock · hack_reputation_penalty)`
(`k`=50), через `racestanding.Service.Adjust(hacker, race, -penalty)` (образец
штрафа полиции `OnRaceShipKilled`). `penalty == 0` — не пишем.

**Маскировка (`up_hide`, TASK-106).** Взлом раскрывает взломщика на этот тик —
`ship.MissileJustFired = true` (тот же транзиентный reveal-путь, что у пуска
ракеты; `hideStealthed` показывает корабль в снапшоте текущего тика). SP
навсегда снимал `up_hide`; в spaceempire — транзиентный reveal (не изобретаем
новый механизм).

**Журнал.** Per-player bus-событие `StationHackedTopic(player)` /
`StationHackedEvent{ShipID, SectorID, StationID, Race, GoodsType, Robbed}`,
транспорт по WS-фрейму `station_hacked` (образец `module_knocked`/`police_scan`).
`Robbed > 0` → «Похищено N ед.»; `Robbed == 0` → «Неудачная попытка взлома»
(списан только `damaged`). Рендер строки журнала — фронт (TASK-100.3.9.6).

## Снятие расового гейта

`configs/equipment.yaml`, `up_hack` (id 122): `race: 6 → 0`. Иначе
`ResolveInstall` (`equipment_effects.go:153`, `e.Race != 0 && e.Race != shipRace`)
блокирует установку игроку на race-0 корабль (`ErrEquipmentWrongRace`).
`min_race_rate: 2` сохранён — репутационный гейт остаётся (AC-10).

## Конфиг (`configs/capture.yaml` → `balance.CaptureConfig`)

| Ключ | Дефолт | Назначение |
|------|--------|-----------|
| `hack_range` | 50 | максимальная дистанция взлома (гейт 8) |
| `hack_goods_min_fraction` | 0.3 | минимум товара (доля `max_stock`) для взлома (гейт 10) |
| `hack_rob_fraction` | 0.15 | доля `stock`, уходящая в лут |
| `hack_damage_fraction` | 0.05 | доля `stock`, уничтожаемая |
| `hack_reputation_penalty` | 50 | `k` штрафа репутации (FR-D5/NFR-004) |

`hack_range` идёт в `sector.Config.HackRange`; остальные — в app-side
`stationRobber` (фракции и `k` не хардкодятся — NFR-004). `EnergyCost` резолвится
из `up_hack.energy_usage` (100) в HTTP-хэндлере (`hackActionEnergyCost`), как
`up_launcher` для ракет.

## Разделение слоёв (persistence-урок .1/.2)

- Списание `station_goods.stock` — через слой рынка (`trade.Service.Rob` /
  `AdjustStock`), НЕ сырым UPDATE.
- Лут в трюм — через `cargo`-путь (`AddCargo` в trade-транзакции), НЕ ad-hoc
  `ship.Save` (Save пишет лишь подмножество колонок).
- `Rob` (списание stock + депозит в трюм) — одна транзакция; штраф репутации —
  отдельная атомарная запись после неё (как police: confiscate-tx, затем
  standing.Adjust). Контейнер-фолбэк — RAM+DB воркера (`s.addContainer`).
- Интеграционный round-trip (`TestIntegration_*`) подтверждает, что списанный
  stock и лут в трюме переживают cold-start (они в БД).

## Где механика срабатывает live (и известное ограничение)

Базис гейта/штрафа — `station_goods.max_stock` конкретной позиции. На живых данных:

- **Производственные станции (`owner_kind 2`) — срабатывают.** Миграция `0042`
  сидит их с реальным запасом (~47% при `max_stock 10000`), гейт «≥30%»
  проходит, штраф репутации `(robbed+damaged)/max_stock · k` не вырождается
  (напр. `940/10000·50 = 5`). Это основной живой таргет взлома.
- **Торговые станции (`owner_kind 4`) — инертны (известное ограничение).**
  Миграция `0044` сидит каждую позицию как `stock 500 / max_stock 1e6`
  (≈0.05%): гейт «≥30% от `max_stock`» не проходит, штраф округлился бы в 0. Это
  resale-рынки без накопления stock. Взлом торговых станций «оживёт» только при
  балансовой правке seed (накопление stock / меньший `max_stock`) — follow-up.

Итого механика **не инертна в целом** (работает на производственных станциях);
инертность осталась лишь на торговых resale-рынках как отдельное ограничение.

## Критерии приёмки

- **AC-8 (гейты):** нет `up_hack` → `ErrEquipmentRequired`; цель не станция (ни
  производственная, ни торговая) → `ErrInvalidAttackTarget`; товар <30% →
  `ErrHackTooLittleGoods`; раса=6 → `ErrInvalidAttackTarget`; `Energy<100` →
  `ErrNotEnoughEnergy`.
- **AC-9 (эффект):** валидная станция (товар ≥30%, в т.ч. производственная
  owner_kind 2) → со `station_goods` списано ~15%+5%; `Level≥2` и есть место →
  лут в трюм, иначе контейнер; своя маскировка отключена (reveal); штраф
  репутации расе станции; событие в Журнал.
- **AC-10 (установка):** `up_hack.race:0`, игрок с race-standing ≥2 → установка
  без `ErrEquipmentWrongRace`.
- **AC-4 (live-срабатывание):** производственная станция ~47% (`max_stock 10000`)
  → взлом проходит гейт, `stock` реально списан, штраф репутации >0 (integration
  `TestIntegration_TradeService_Rob_ProductionStation_RoundTrip` + app-unit).
