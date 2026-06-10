# station_types + station_goods_types → каталог типов станций и рецепты (8.15)

Порт справочника типов станций и производственных цепочек из оригинального
StarWind. Источники:
- `starwind/sql/db.sql` `station_types` (168 строк) — `id, name, race_id,
  type(kind), space, hull, shield, sellable`.
- `starwind/sql/db.sql` `station_goods_types` (864 строки) — `station_type_id,
  goods_type_id, cycle_type, cycle_time, goods_count_cycle, goods_max_count,
  type`.
- SP `To_Production` — рантайм-семантика цикла (сверка с `economy/production`).

## Конвертер

`cmd/starwind-tools/convert-station-types -sql <db.sql> -out configs/station_types.yaml`
читает обе таблицы (дамп фактически UTF-8) и пишет один файл с двумя секциями:
`station_types:` (каталог) и `recipes:` (рецепты в формате, который уже парсит
`internal/balance`).

Рецепты держатся отдельно от `balance.yaml`, потому что `convert-balance`
перегенерирует `balance.yaml` только с `goods_types` (затёр бы `recipes:`).
`app.Run` грузит goods из `balance.yaml`, каталог+рецепты из
`station_types.yaml`, и собирает `balance.New(goods, recipes)` —
`New` валидирует каждую строку рецепта против каталога товаров.

## Отображение богатой модели на `balance.Recipe`

`station_goods_types` богаче целевой модели (`Recipe{Inputs, Outputs,
CycleTime}` — один `CycleTime` на рецепт). Маппинг (только `cycle_type=1`,
производство):

- **Группировка** строк по `station_type_id`.
- **`CycleTime` рецепта** = `cycle_time` самой медленной выходной линии
  (период выпуска основного продукта).
- **Входы** (`type=0`): количество нормируется на период рецепта —
  `qty·recipeCycle/lineCycle`. В данных циклы кратны, поэтому факторы целые
  (напр. ст. 5: входы cycle 9720 при выходе 29160 → ×3). Это rate-preserving
  и совпадает с контрактом движка: «за один `CycleTime` потребить все Inputs,
  затем выдать все Outputs».
- **Выходы** (`type=1`): `qty` нормируется так же (для определяющего выхода
  фактор 1), `Max` = `goods_max_count` (потолок склада, проверяется на старте
  цикла в `hasOutputRoom`).
- `cycle_time` в секундах (в оригинале 86400 = ровно сутки у самых медленных).

## Отступления MVP (отложено)

- **`type=-1` (вторичный upkeep)** — расовые варианты (101–121, 201–221, …)
  потребляют еду/виски (`goods 40/60–69`) строкой `type=-1` с отрицательным
  `cycle_time`. Пропускается (расовый вариант = база без upkeep). Деление не
  целочисленно (5760/1728≈3.33) — нужен отдельный механизм потребления.
- **`cycle_type=0/2` (постройка/перестройка)** — нужны для механики
  строительства станций (расширение 8.7), не для производства. Пропущены.
- **Один `cycle_time` на рецепт** — оригинал имеет per-line `cycle_time`
  (независимые скорости линий). MVP сводит к одному периоду нормировкой.
- **Расовые варианты** хранятся как отдельные `station_type` (id `base+100·race`)
  с дублирующими base рецептами (без upkeep). Не схлопываются в base.
- **Сидинг станций** не менялся (station #1 = type 1 = Электростанция);
  осмысленная расстановка типов — follow-up (часть 8.19).
- **Acceptance-паритет с живым `To_Production`** (MySQL sofer/1) — не
  прогонялся (внешний MySQL, 7.6a). Сверены значения station_type 1/5 по дампу.

## Реконсиляция с `economy/production`

Движок (`Service.Tick`) уже исполняет multi-input/multi-output + output-cap
рецепты: проверяет наличие входов (`hasInputs`) и место под выходы
(`hasOutputRoom`) на старте, по истечении `CycleTime` в одной транзакции
списывает входы и начисляет выходы. Сгенерированные рецепты ложатся на эту
модель без изменений движка. Подробности рантайма — `production.md`.
