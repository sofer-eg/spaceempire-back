# Спецификация: Станции (phase 3.1)

Доменные типы и persistence для статичных объектов сектора — станций
(фабрик), верфей, торговых станций и пирбаз.

Эти объекты не двигаются, не уничтожаются мирным игроком, занимают
фиксированную точку в секторе и являются целью стыковки (`dockable`).

## Источник в старом StarWind

`/home/sof/projects/go/src/starwind/sql/db.sql`, таблицы:
`stations`, `shipyards`, `trade_stations`, `pirbases`.

В фазе 3.1 портируем **только структуру и загрузку**. Бизнес-логику
(производство, торговля, постройка, аренда) портируем в фазах 3.2-3.8.

## 1. Station (фабрика)

| Старое поле        | Новое поле       | Тип            | Заметка                                                  |
|--------------------|------------------|----------------|----------------------------------------------------------|
| `ID`               | `ID`             | `StationID`    | PRIMARY KEY                                              |
| `owner`            | `OwnerID`        | `*PlayerID`    | NULL = ничейная (раса)                                   |
| `type`             | `Type`           | `StationType`  | См. balance / `config.tp.php`; в 3.1 хранится как `int` |
| `shield`           | `Shield`         | `int`          | Текущее значение щита                                    |
| `hull`             | `HP`             | `int`          | Текущее значение корпуса                                 |
| `sector`           | `SectorID`       | `SectorID`     |                                                          |
| `pos_x`, `pos_y`   | `Pos`            | `Vec2`         |                                                          |
| `race`             | `Race`           | `int`          | 0=ничей, 1-4 = расы. Деталь — фаза 6.2 (Relations)       |
| `in_progress`      | —                | —              | Производство; перенесём в фазу 3.6                       |
| `next_cycle_time`  | —                | —              | Производство; перенесём в фазу 3.6                       |
| `builded`          | `Built`          | `bool`         | `1` → `true`                                             |
| `type_old`         | —                | —              | Legacy; не портируем                                     |
| `arenda_ppd`       | —                | —              | Аренда; перенесём в фазу 6.4                             |

## 2. Shipyard (верфь)

| Старое поле        | Новое поле     | Тип             | Заметка                       |
|--------------------|----------------|-----------------|-------------------------------|
| `ID`               | `ID`           | `ShipyardID`    |                               |
| `owner`            | `OwnerID`      | `*PlayerID`     |                               |
| `race`             | `Race`         | `int`           |                               |
| `sector`           | `SectorID`     | `SectorID`      |                               |
| `pos_x`, `pos_y`   | `Pos`          | `Vec2`          |                               |
| `shield`           | `Shield`       | `int`           |                               |
| `hull`             | `HP`           | `int`           |                               |
| `builded`          | `Built`        | `bool`          |                               |

## 3. TradeStation (торговая станция)

| Старое поле        | Новое поле        | Тип                | Заметка                                |
|--------------------|-------------------|--------------------|----------------------------------------|
| `ID`               | `ID`              | `TradeStationID`   |                                        |
| `owner`            | `OwnerID`         | `*PlayerID`        |                                        |
| `type`             | `Type`            | `int`              |                                        |
| `shield`           | `Shield`          | `int`              |                                        |
| `hull`             | `HP`              | `int`              |                                        |
| `sector`           | `SectorID`        | `SectorID`         | В старой схеме `UNIQUE` — на сектор ≤1 |
| `pos_x`, `pos_y`   | `Pos`             | `Vec2`             |                                        |
| `race`             | `Race`            | `int`              |                                        |
| `builded`          | `Built`           | `bool`             |                                        |
| `arenda_ppd`       | —                 | —                  | Фаза 6.4                               |
| `tax_race`         | —                 | —                  | Фаза 3.4                               |
| `tax_owner`        | —                 | —                  | Фаза 3.4                               |

## 4. Pirbase (пиратская база)

| Старое поле        | Новое поле     | Тип            | Заметка                                          |
|--------------------|----------------|----------------|--------------------------------------------------|
| `ID`               | `ID`           | `PirbaseID`    |                                                  |
| `sector`           | `SectorID`     | `SectorID`     |                                                  |
| `pos_x`, `pos_y`   | `Pos`          | `Vec2`         |                                                  |
| `hull`             | `HP`           | `int`          |                                                  |
| `shield`           | `Shield`       | `int`          |                                                  |
| `angle`            | `Angle`        | `float64`      | Уникально для пирбаз                             |
| `builded`          | `Built`        | `bool`         |                                                  |
| `race`             | `Race`         | `int`          | По умолчанию 6 (пираты)                          |

Пирбаз нет в seed старой схемы (таблица пуста). В новой версии мы
заводим 1 экземпляр для seed-мира. Логика продажи рабов — фаза 5.6.

## Domain интерфейс DockableObject

```go
type DockableObject interface {
    ObjectID() EntityRef        // typed (kind, id) для AOI/snapshot
    ObjectSector() SectorID
    ObjectPos() Vec2
}
```

Реализуют: `Station`, `Shipyard`, `TradeStation`, `Pirbase`.
В фазе 3.2 — `Ship.Dock(target DockableObject)` уже будет иметь
универсальный приёмник.

## EntityKind расширение

```go
const (
    EntityKindShip         EntityKind = 1
    EntityKindStation      EntityKind = 2
    EntityKindShipyard     EntityKind = 3
    EntityKindTradeStation EntityKind = 4
    EntityKindPirbase      EntityKind = 5
)
```

## Persistence (фаза 3.1)

- **Immediate save**: создание/удаление (постройка/разрушение).
  В 3.1 не реализуем триггеры; готовим методы `Create` / `Delete` / `Save`.
- **Periodic save**: HP, Shield — раз в 5с по dirty-set воркера.
  В 3.1 реализуем `BatchUpdate`, но воркер пока не помечает их как dirty
  (HP/Shield статичны — фаза 4 расставит).
- **Reconstructable**: ничего (всё хранится).

Загрузка: воркер при cold start вызывает `LoadAll(ctx, sectorID)` для
каждого из 4 типов, индексирует по `Pos` (для будущих коллизий /
range-запросов в фазе 3.2 docking) и по `EntityRef`.

## Seed

В одну миграцию `0004_stations.sql`:

| Сектор   | Stations | Shipyards | TradeStations | Pirbases |
|----------|----------|-----------|---------------|----------|
| 1        | 1        | 1         | 1             | 0        |
| 2        | 1        | 0         | 1             | 0        |
| 3        | 1        | 0         | 1             | 0        |
| 4        | 1        | 1         | 0             | 0        |
| 5        | 1        | 0         | 0             | 1        |
| **Итого**| **5**    | **2**     | **3**         | **1**    |

Координаты — внутри `bounds (-1000..1000, -1000..1000)` каждого сектора
(из seed `0002_world_topology.sql`). Конкретные `pos_x/pos_y` подбираются
так, чтобы они не пересекались с воротами (ворота на ±900 от центра по
оси).

## API

В фазе 3.1 — никаких новых HTTP-эндпоинтов. Станции попадают во фронт
**через WS-snapshot AOI** (новое поле `StaticObjects` в snapshot,
рядом с `Ships`). Отдельный REST `/api/sector/{id}/objects` не делаем
(одно место доставки состояния сектора).

## Критерии приёмки (из tasks/phase3-01-stations-domain.md)

- [x] 4 типа объектов в БД (4 таблицы, миграция goose)
- [x] Sector worker при старте видит станции в своём секторе
  (`LoadAll` на cold start)
- [x] Доставка станций во frontend — через WS-snapshot (вместо
  отдельного HTTP-эндпоинта)

Тесты:

- `TestIntegration_StationsRepo_LoadAll` — testcontainers, миграция,
  seed, LoadAll возвращает корректное число объектов по сектору
- `TestUnit_SectorWorker_LoadsStationsOnStart` — на старте воркера
  загруженные через мок repo станции попадают в state и AOI-snapshot
