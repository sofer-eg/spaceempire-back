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
| 10 | товара на целевой позиции `>= hack_goods_min_fraction` (0.3) от хак-базиса (см. «Целевой товар»: собственный `max_stock` для owner_kind 2, производственный эталон для TS) | `ErrHackTooLittleGoods` (422) |

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
пират (`richestRobbable`). Гейт «≥30%» применяется к **хак-базису** этой позиции
(см. ниже), а не к сырому `max_stock`.

**Хак-базис (знаменатель гейта/штрафа), TASK-128.** Зависит от типа станции:

- **Производственная станция (`owner_kind 2`) и пирбаза** — базис = собственный
  `station_goods.max_stock` товара (реалистичный потолок запаса, миграция 0042
  ≈10000). Поведение байт-идентично до-TASK-128 (каждая позиция — кандидат,
  гейт `basis <= 0` отсекает вырожденный потолок).
- **Торговая станция (`owner_kind 4`)** — собственный `max_stock` у TS это
  resale-cap `1e6` (миграция 0044), недостижимый потолок перепродажи. Базис —
  **производственный эталон** товара: `max(max_stock)` того же товара по всем
  производственным станциям (`ProductionRefMaxes`, `SELECT goods_type_id,
  max(max_stock) FROM station_goods WHERE owner_kind=2 GROUP BY goods_type_id`).
  Читается прямо в транзакции `Rob` (взлом — редкая операция, не hot-path; live
  запрос вместо кэша избегает stale-данных).

**Исключение непроизводимых товаров (TS), TASK-128 AC-2.** Товар, который никто
не производит (нет строки `owner_kind 2` → нет эталона: пушки 301–322, торпеды
23/24, сокровища 101–105/112–114, гипертопливо 70) на торговой станции
**исключается из выбора цели** `richestRobbable`. Критично: исключение именно из
ВЫБОРА, а не «непроходимый гейт» — набитая непроизводимым товаром позиция
(высокий `stock`) иначе перехватила бы `richestGood`, провалила гейт и прикрыла
грабимые производимые позиции рядом. `richestRobbable` выбирает самую жирную кучу
ТОЛЬКО среди производимых товаров. Resale-cap `1e6` у TS при этом не меняется —
рынок buy/sell цел, reseed не нужен.

**Расчёт** (`trade.RobIn`, внутри транзакции `app.hackRaider` — TASK-160):
- `robbed = floor(hack_rob_fraction · stock)` (0.15),
- `damaged = floor(hack_damage_fraction · stock)` (0.05),
- клэмп как в SP: если `robbed+damaged > stock` → `robbed = 0`; если
  `damaged > stock` → `damaged = 0`,
- `AdjustStock(station, gtype, -(robbed+damaged))` — списываем украденное +
  испорченное со `stock` (испорченное просто уничтожается, награбленное — в лут).

**Лут** (faithful SP): при `Level(up_hack) >= 2` **и** есть место в трюме —
`robbed` кладётся в трюм взломщика (`AddCargo` в той же транзакции, что и
списание stock → атомарно; `Delivered=true`). Иначе (`Level 1` или трюм полон)
— контейнер с `robbed` единицами создаётся рядом со станцией **в той же
транзакции** (`containers.SpawnIn`, TASK-160). Позицию, TTL и сектор выбирает
воркер и передаёт в `sector.LootDrop`; сам он контейнер не пишет — получает
готовый `RobResult.Container` и только добавляет его в live-набор
(`s.addContainer`).

До TASK-160 контейнер спавнился воркером **после** возврата `Rob` отдельной
транзакцией, и товар терялся в двух случаях: успешный `Rob` с упавшим
`SpawnContainer`, и дедлайн `RepoTimeout`, сработавший при уже летящем COMMIT
(pgx рвёт соединение и отдаёт `DeadlineExceeded`, Postgres коммитит). Теперь оба
случая безопасны: либо коммитятся оба факта, либо ни один. Остаточный эффект
дедлайна — только RAM: `addContainer` не выполнился, контейнер лежит в БД
невидимым до следующего cold-start (`LoadAll`), см. `logRobError`.

**Репутация.** Штраф расе станции (только основные расы 1–5) пропорционально
доле изъятого: `penalty = round((robbed+damaged)/basis · hack_reputation_penalty)`
(`k`=50), где `basis` — хак-базис цели (`RobOutcome.MaxStock`: собственный
`max_stock` для производственной станции, производственный эталон для TS,
TASK-128), через `racestanding.Service.Adjust(hacker, race, -penalty)` (образец
штрафа полиции `OnRaceShipKilled`). `penalty == 0` — не пишем. Штраф считается
app-side (`stationRobber.Rob`) из `RobOutcome.MaxStock` — `Rob` кладёт в это поле
уже вычисленный базис, поэтому app-слой менять не нужно.

**Без клэмпа (TASK-128 AC-3).** Формула штрафа НЕ ограничивается сверху `min(1,
…)`. У TS resale-cap `1e6` позволяет игроку накопить `stock >> basis`
(производственного эталона), поэтому `(robbed+damaged)/basis` может превысить 1, а
штраф — `k=50`. Это осознанное решение заказчика: большой куш → большой штраф.

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
- `Rob` (списание stock + депозит в трюм + контейнер-фолбэк) — одна транзакция,
  которой владеет `app.hackRaider` (образец `staticInstaller`/`ordnance`); штраф
  репутации — отдельная атомарная запись после неё (как police: confiscate-tx,
  затем standing.Adjust). Воркер получает созданный контейнер и добавляет его
  только в RAM.
- Интеграционный round-trip (`TestIntegration_*`) подтверждает, что списанный
  stock и лут в трюме переживают cold-start (они в БД).

## Где механика срабатывает live (TASK-128)

Хак-базис — производственный потолок товара (собственный `max_stock` для
производственной станции, производственный эталон для TS). На живых данных:

- **Производственные станции (`owner_kind 2`) — срабатывают.** Миграция `0042`
  сидит их с реальным запасом (~47% при `max_stock 10000`), гейт «≥30%»
  проходит, штраф репутации `(robbed+damaged)/max_stock · k` не вырождается
  (напр. `940/10000·50 = 5`). Основной живой таргет взлома.
- **Торговые станции (`owner_kind 4`) — грабимы для накопленных производимых
  товаров (TASK-128).** Базис TS — производственный эталон товара (не resale-cap
  `1e6`). Когда игроки продали на TS столько производимого товара, что его `stock
  ≥ 30% эталона` (напр. батарейки ≥ 3000 при эталоне 10000), гейт проходит и TS
  реально грабится: списание `stock` + штраф `>0`. Непроизводимые товары
  (пушки/торпеды/сокровища/гипертопливо) на TS не грабятся вовсе (нет эталона —
  исключены из выбора цели).
- **Пустой seed TS — не грабится (нижняя граница).** Миграция `0044` сидит каждую
  позицию как `stock 500 / max_stock 1e6`. `richestRobbable` целит в самую жирную
  производимую кучу — при флэтовом seed это батарейки (наименьший производимый
  `goods_type_id` при `stock 500`), эталон 10000. `500 < 30% · 10000 = 3000` →
  `ErrTooLittleGoods`. Пока игроки не набьют прилавок производимым товаром, TS
  остаётся неграбимой — это ожидаемое поведение, а не инертность.

Итого механика активна и на производственных станциях, и на торговых:
грабимость TS определяется фактически накопленным запасом производимого товара
относительно производственного эталона.

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
- **TASK-128 (грабимость TS):** торговая станция с производимым товаром `stock ≥
  30%` производственного эталона → взлом проходит, `stock` списан, знаменатель
  штрафа = эталон, resale-cap `1e6` не тронут (integration
  `TestIntegration_TradeService_Rob_RoundTrip`); непроизводимый товар не
  перехватывает выбор цели (unit `..._UnproducedGoodExcluded`); пустой seed TS не
  грабится (integration `..._TradeStationSeed_TooLittle`); производственная
  станция байт-идентична (unit `..._ProductionStation_UsesOwnMaxStock`).
