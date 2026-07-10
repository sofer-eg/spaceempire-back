# DestroyModule — выбивание модулей в бою (TASK-100.3.9.1)

Порт боевой механики SP `DestroyModule` (`starwind/sql/db.sql:10012`,
settings `db.sql:2295`) в combat-конвейер spaceempire. ЧТЗ — doc-4 §3
«Механика A» (FR-A1…A7), §5.1 (числа), §6 (AC-1/2/3).

Фундамент зонтичной подсистемы захвата/взлома TASK-100.3.9: единственный
способ лишить корабль модуля `up_shield` и открыть его для захвата (.4).

## Что делает оригинал

`DestroyModule(object_id_full, object_owner_id, shield_charge, hull_integrity)`
вызывается при каждом попадании по кораблю. Константы (захардкожены в SP):

| Параметр | Значение |
|---|---|
| `critical_shield_charge` | 0.2 |
| `critical_hull_integrity` | 0.7 |
| внешний базовый шанс (`external_standart_hc`) | 0.2 |
| внутренний базовый шанс (`internal_standart_hc`) | 0.1 |

Логика:
1. **Главный гейт:** `if shield_charge > critical_shield_charge → leave`
   (щит выше 20% — ничего не выбивается).
2. **Внешний ролл:** `chance = (1 - shield/0.2) * 0.2` (растёт 0→0.2 по мере
   падения щита к нулю). При `rand() < chance` — удаляется случайный внешний
   модуль (в оригинале приоритет `up_weapons`→`up_turrets`→`updates` c
   `ct_updates.position=2`).
3. **Внутренний ролл:** если `hull_integrity > 0.7` → шанс 0, иначе
   `chance = (1 - hull/0.7) * 0.1`. При `rand() < chance` — удаляется случайный
   внутренний модуль (`ct_updates.position=1`, сюда входит `up_shield`).
   Приоритета выбить именно `up_shield` НЕТ — лотерея среди внутренних.
4. Оба ролла независимы; за один вызов может выпасть до двух модулей (один
   внешний + один внутренний). Владельцу шлётся `messages_sys` («… уничтожен»).
   Лута нет — модуль теряется безвозвратно.

## Реализация в spaceempire

**Числа — балансовое решение** (ЧТЗ C-01, NFR-004): вынесены в
`configs/capture.yaml` → `balance.CaptureConfig` (не захардкожены).
Паритет **относительный** — профиль (пороги/соотношения) сохранён, точные
числа совпадают с оригиналом как дефолты.

- `combat.KnockModule(ship, shieldFrac, hullFrac, rng, cfg)`
  (`internal/combat/knockmodule.go`) — чистая функция: гейт + два ролла,
  мутирует `ship.Equipment` (пересобирает **свежий** слайс, чтобы снапшот,
  алиасящий старый, не портился — NFR-003), возвращает выбитые модули.
  `cfg combat.KnockConfig` несёт 4 скаляра + `Positions map[string]int`
  (Type→слот, строится один раз из каталога — без per-tick lookup, R-02).
  `rng` — `combat.RNG` (`Float64()`, сидируемый в тестах).
- Позиция модуля (FR-A7): выбран путь **«worker консультирует каталог»** —
  `Positions` кладётся в конфиг из `balance.Equipments` в `app`, а не новое
  поле в `domain.InstalledEquipment` (минимальный blast-radius: без правок
  persistence JSONB и всех мест конструирования модуля).
- Вызов после урона во всех боевых путях (`internal/sector/`):
  `combat.go` (лазеры), `missiles.go`, `drones.go`, `torpedos.go` (splash,
  по каждому уцелевшему кораблю), `lasertowers.go` — только если цель это
  корабль и НЕ убита этим попаданием (`w.knockModules`, `knock.go`).
- После выбивания (`knockModules`): пересчёт фита (`sector.Refitter` →
  `app.equipmentRefitter` поверх `ApplyEquipmentEffects`+`EnergyDelta`, FR-A5),
  `immediateSave`, событие в Журнал владельцу (per-player bus topic
  `ModuleKnockedTopic` → WS-фрейм `module_knocked` в `api/ws.go`; SPA-консьюмер
  отложен на фронт-подзадачу .5).

## Завязка щита на up_shield (FR-A6 / AC-3) — долговечный инвариант

Оригинал требует `up_shield` для работы щита. В spaceempire `MaxShield`
берётся из класса + буст `up_shield` (up_shield — **буст**, не пререквизит),
поэтому глобальный гейт «нет up_shield → Shield 0» отрегрессил бы NPC/фикстуры
с классовым щитом. Решение: **коллапс щита — персистентный per-ship инвариант**,
а не same-event override, через маркер `domain.Ship.ShieldGeneratorDestroyed`:

- **ставится один раз**, когда `up_shield` попал в выбитый набор (`knockModules`);
- **консультируется БЕЗУСЛОВНО после каждого `Refit`** (в `knockModules`) →
  форсит `MaxShield=0, ShieldRecharge=0, Shield=0`. Это ключевой момент: `Refit`
  (пересчёт фита после ЛЮБОГО выбивания, FR-A5) восстановил бы `MaxShield =
  cls.Shield` (классовый база), поэтому позднее НЕсвязанное выбивание (напр.
  `up_launcher`) без маркера «воскресило» бы щит. Маркер держит его сбитым;
- **ПЕРСИСТИТСЯ** — колонка `ships.shield_generator_destroyed BOOLEAN NOT NULL
  DEFAULT FALSE` (миграция `0058`), читается в `LoadAll`, пишется в
  equipment-пути (`SaveEquipment`). Так коллапс переживает и последующие
  knockoff в сессии, и cold-start;
- **сервер-внутренний** — в Ship DTO НЕ добавлен (фронт гейтит по `shield===0`);
- **сбрасывается при осознанном ре-аутфите на верфи** (`UpdateShipEquipmentCommand`
  ставит `false`, `outfitShip` пишет `false`) — фит перестраивается с нуля,
  «ремонт» на верфи возвращает щит по новому оборудованию (RAM и БД согласованы).

`combat.ChargeShield` уже no-op при `MaxShield<=0` → щит не регенерирует, это и
открывает захват (.4, гейт по отсутствию модуля `up_shield`). Маркер также
различает «never-had up_shield (классовый щит) vs up_shield-выбит (щит 0)» —
иначе `Refit` всегда вернул бы классовый база. Ни `chargeShields`, ни фикстуры
без knock не затрагиваются.

## Персистентность (CRITICAL-1)

`immediateSave`→`repo.Save`/`BatchUpdate` пишут только динамику (`hp, shield,
pos, …`) и НЕ пишут `equipment/max_shield/shield_recharge`. Поэтому после
выбивания `knockModules` персистит через **equipment-путь** —
`w.immediateSaveEquipment` → `ShipRepo.SaveEquipment` (`saveEquipmentSQL` пишет
`equipment` без up_shield + `max_shield`/`shield_recharge` + маркер; клампит
`shield=LEAST(shield,max_shield)` → при `max_shield=0` обнуляет shield). Без
этого cold-start (`LoadAll`) вернул бы корабль с up_shield и восстановленным
щитом → knockoff отменён. `sector.ShipRepo` расширен методом `SaveEquipment`.

## Acceptance (unit)

- `combat/knockmodule_test.go` — гейты/роллы на scripted `queueRNG` + seed-
  воспроизводимость + non-aliasing (AC-1/2/3, §5.2).
- `sector/knock_test.go` — интеграция в боевом пути: щит >20% → без выбивания;
  щит down → внешний модуль + событие в Журнал; корпус <70% → up_shield выбит,
  щит коллапсирует и не регенерирует; **cross-event** (up_shield, затем
  up_launcher) — маркер держит щит сбитым несмотря на refit-revive; **persist**
  — knockoff идёт через `SaveEquipment` (equipment + max_shield=0 + маркер).
- `persistence/ships` (`TestIntegration_..._ShieldKnockoffRoundTrips`) —
  `SaveEquipment`+`LoadAll` round-trip: cold-start грузит корабль без up_shield,
  `max_shield=0`, `shield_generator_destroyed=true`.
- `app/refit_test.go` — refit снимает буст выбитого модуля, clamp Shield.
- `balance/capture_loader_test.go` — capture.yaml → CaptureConfig + дефолты.
- `sector/capture_chain_test.go` (`TestUnit_CaptureChain_KnockShieldThenCapture`,
  TASK-100.3.9.7) — сквозной сценарий §5.2: боевой knockoff `up_shield` открывает
  корабль к захвату (.1↔.4 компонуются).

## Отложено / вне scope

- Лут-контейнер из выбитого модуля (ЧТЗ C-02) — модуль теряется безвозвратно
  (faithful).
- SPA-рендер журнальной строки «Модуль … уничтожен» — фронт-подзадача
  TASK-100.3.9.5 (бэковый bus/WS-фрейм эмитится уже сейчас).
