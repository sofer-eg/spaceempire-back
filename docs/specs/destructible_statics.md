# destructible_statics — уничтожимая статика + зарядка щитов (phase 6.2b)

Follow-up боевых задач фазы 4: до 6.2b бой ограничен ship-целями. Здесь статика
(stations / shipyards / trade_stations / pirbases / laser_towers) получает урон,
заряжает щиты и разрушается. Порт идей `TO_ObjectShieldCharge` и ship-ветки
`KillObject`, обобщённых на не-ship объекты, с hostility-гейтом из 6.2/6.2a.

## Что в оригинале

- `TO_ObjectShieldCharge` — отдельный SP, тикающий щиты статики (аналог
  `TO_ShipShieldCharge` для кораблей): `shield += charge`, clamp по max.
- `KillObject` с `object_type != 5` — ветки разрушения станций/башен/ворот.
- Оружие в старой схеме бьёт по любому объекту; враждебность — по race-standing
  и owner-relations.

## MVP в Go (это 6.2b)

### Унифицированная модель

`domain.DestructibleStatic{ Ref, Pos, OwnerID, HP, Shield, MaxShield,
ShieldRecharge }` — одна combat-запись на статик-объект, реализует
`combat.Damageable`. Строится на cold-start из `SectorStatics`
(`DestructiblesFromStatics`) и живёт в `sectorState.destructibles`
(map по `EntityRef`). Это убирает 5× per-kind дублирование в combat/charge/kill.

**Combat-состояние RAM-only** — не персистится в этом MVP: рестарт
восстанавливает статику full/неразрушённой. Загружаются только `max_shield`
и `shield_recharge` (миграция `0028`), нужные для зарядки.

### Урон (общий код)

Логика «щит, затем HP» вынесена в `domain.applyDamage(&hp, &shield, dmg)` —
её используют и `Ship.TakeDamage`, и `DestructibleStatic.TakeDamage`.
`combat.FireLaserAt(attacker, ref, pos, hp, Damageable)` обобщает
`FireLaser` (range/energy/damage в одном месте); `FireLaser` — тонкая обёртка.

### Таргетинг и hostility

`fireLasers` диспетчеризует: ship-цель → старый путь, static-цель
(`isStaticTargetKind`: station/shipyard/trade_station/pirbase/laser_tower) →
`fireLaserAtStatic`. Гейт — `w.hostile(static.OwnerID, attacker)` (6.2a
owner-оракул поверх relations; mutual). nil-owner (NPC/пираты) или
friendly/neutral → выстрел не проходит, `AttackTarget` сбрасывается
(дружественная/нейтральная статика неуязвима).

**Ворота (TASK-110)** входят в `IsStaticTargetKind` и проходят через отдельный
гейт `staticAttackable`: у ворот нет владельца, поэтому owner-оракул ответил бы
«не враждебно» и сделал бы их неуязвимыми — то самое состояние, которое задача и
снимает. Ворота — общественная инфраструктура: стрелять по ним может любой, и
связь теряют все. Подробнее — «Ворота» ниже.

### Зарядка и разрушение

`chargeStatics` в `tickSector` (рядом с `chargeShields`): `ChargeShield`
добавляет `ShieldRecharge` до `MaxShield`. При HP=0 `killStatic`: убирает из
`destructibles` (WS-removal) и из `s.statics` (`removeStaticFromLayout` — башня
перестаёт стрелять, станция перестаёт быть dock/trade-целью) + `entity_killed`.

### WS / фронт

`Snapshot.Destructibles` (полный список) + `Patch.StaticsUpdated/StaticsRemoved`
(глобально, без AOI — события редкие) → dto + ws-encoder. Фронт
(`useWorldState`) держит `staticCombat`-карту, убирает уничтоженную статику из
`statics`; `SectorCanvas` рисует шит-бар над повреждённой статикой.

#### Живое состояние в welcome-кадре (TASK-186)

Welcome-кадр `dto.StaticsMessage` несёт **два набора**:

- `statics` — иммутабельная раскладка сектора (`cloneStatics(s.statics)`,
  разделяется между подписчиками). Её `hp` — спавновое значение.
- `destructibles` — живые `hp/shield/maxShield` из `s.destructibles`, в том же
  `dto.DestructibleStatic`, что и dirty-патч `staticsUpdated`
  (`dto.DestructiblesFromDomain` — один энкодер на оба потребителя).

До TASK-186 ехал только первый, и клиент после **любого** реконнекта или прыжка
в соседний сектор и обратно читал спавновые числа: `TO`-урон в раскладку не
пишется никогда, а `staticCombat` на фронте обнулялся в `socket.onopen` и на
смене сектора. Профиль наихудший — число верно, пока не было боя, и ложно ровно
после боя. `sendStatics` вызывается и на подключении, и заново на
`PlayerHandoffEvent` (`wsPushLoop`), поэтому набор едет в обоих случаях.
`GET /api/state` отдаёт то же поле `destructibles` рядом с `statics`.

**Почему отдельным полем, а не слиянием в раскладку:** раскладка иммутабельна и
разделяется между подписчиками, слияние потребовало бы копии всего сектора на
каждое подключение. Живой набор — уже сделанная тиком value-копия
(`snapshotDestructibles`), поэтому подключение платит только за сериализацию.
Замер (`internal/api/dto/statics_bench_test.go`, 40 статик-объектов — жирнее
любого реального сектора, i5-1135G7): раскладка `StaticsFromDomain`
1007 ns/op, 3328 B/op, 4 allocs/op — то, что подключение платило и раньше;
добавка `DestructiblesFromDomain` 470 ns/op, 1792 B/op, **1** alloc/op.

**Максимума корпуса на проводе нет и не будет, пока не появится ремонт.** У
статики нет колонки максимального корпуса ни в `domain.DestructibleStatic`, ни в
БД, но спавновое `hp` из раскладки **и есть** де-факто максимум: урон статике не
персистится, а корпус не регенерирует (вверх идёт только щит через
`ChargeShield`; `applyDamage` выходит рано на `dmg <= 0`, так что и лечение
отрицательным уроном закрыто). Поэтому бар в панели «Бой» рисуется как
`destructibles.hp / statics.hp`. Два следствия: после рестарта сервера живое
значение возвращается к спавновому (RAM-only, см. «Combat-состояние RAM-only»
выше), и если появится ремонт (TASK-67) или персист урона — знаменатель
перестанет быть максимумом и потребуется настоящий `maxHP`. `maxShield` в
`dto.DestructibleStatic` уже есть.

**Остаётся незакрытым:** статика, вышедшая из big-radar окна и вернувшаяся
(10.20 L2, радарно-гейтятся башни/спутники/джаммеры), приезжает в
`staticsAdded` только раскладкой — живого состояния при этом в патче нет, и до
следующего dirty-события у клиента снова спавновое число. Тот же корень, другой
триггер; welcome/реконнект/прыжок закрыты.

### Ворота (TASK-110)

Ворота были единственной статикой вне таргет-набора (ЧТЗ C-04). Отличие от
прочей статики одно, но определяющее: **у ворот две конечные точки** — по одной
в каждом связанном секторе, и каждый воркер владеет только своей.

- **Боевое состояние** — колонки `gates.hp/shield/max_shield/shield_recharge`
  (миграция `0062`, дефолты 250000/100000/100000/200: дороже деплойных объектов,
  но на порядки дешевле станции — потеря ворот рвёт связь секторов, поэтому это
  должна быть скоординированная операция, а не работа одного фрегата).
- **Две точки, два пула HP.** `seedGateEndpoints` регистрирует в каждом секторе
  свою сторону как `DestructibleStatic` с позицией `EndpointPos(sectorID)`.
  Общего живого пула HP быть не может — один writer на сектор, — поэтому стороны
  получают урон независимо. Ворота при этом **не входят** в
  `domain.SectorStatics`: они — топология мира, а не layout сектора, и все
  консьюмеры (прыжки, маршруты, `/api/world`) уже ищут их в топологии.
- **Гибель любой стороны убивает ворота.** `killStatic` → `killGate`:
  `world.Topology.DestroyGate(id)` вырезает ребро из графа (и бампает
  `Version()`), затем `worldRepo.MarkDestroyed` персистит обломок. Возврат
  `false` от `DestroyGate` означает «вторая сторона уже умерла» — сever и персист
  уже случились.
- **Граф — единственный авторитет.** `Topology` теперь мутируемая в одной точке
  (RWMutex + `Version()`), а `PathRouter` кэширует BFS **по версии графа**: при
  изменении версии кэш выбрасывается целиком и BFS перезапускается на разорванном
  графе. Без этого корабли продолжали бы маршрутизироваться через несуществующие
  ворота. `Gates()/Gate()/GatesInSector()` не возвращают разрушенные, поэтому
  `JumpCommand` отказывает `ErrInvalidGate` без отдельной проверки «это обломок».
- **Вторая сторона** убирается своим же воркером: `sweepDestroyedGates` в тике
  сверяет свои gate-endpoint'ы с топологией. Кросс-воркерных сообщений не нужно —
  топология общая, каждый примиряет свою RAM сам.
- **Персист разрушения** — `WHERE NOT destroyed` в `loadGatesSQL`: разрушенные
  ворота просто не попадают в топологию при cold-start, поэтому разрыв переживает
  рестарт, а фильтровать флаг никому не приходится. Ремонт — TASK-67.

**Продуктовое следствие (решение заказчика):** разрушенные ворота остаются
разрушенными — авто-респавна нет. Связность мира может быть необратимо (до
ремонта) урезана действиями игроков.

**Фронт вне scope задачи** (метки back/db): SPA берёт ворота из `/api/world`,
загружаемого один раз, поэтому до перезагрузки страницы обломок ещё рисуется, а
клик «Прыжок» получает 4xx. Follow-up — TASK-163.

## Покрытие тестами

- `TestUnit_StaticCombat_DamagesHostileStation` — шит→HP.
- `TestUnit_StaticCombat_DestroysHostileStation` — удаление из combat-набора и
  рендер-слоя.
- `TestUnit_StaticCombat_FriendlyInvulnerable` — гейт hostility.
- `TestUnit_StaticCombat_ShieldRecharges` — зарядка + clamp.
- Миграция `0028` верифицирована на PG16.
- TASK-186 (живое состояние в welcome-кадре):
  `TestUnit_WS_WelcomeCarriesLiveStaticCombat` — заряженный щит в кадре против
  спавнового в раскладке; `TestUnit_WS_WelcomeOmitsStaticsWithoutLiveState` —
  источник истины именно живая карта (мертвый объект не приезжает живым);
  `TestUnit_WS_HandoffWelcomeCarriesLiveStaticCombat` — тот же набор после
  прыжка; `TestUnit_State_CarriesLiveStaticCombat` — `GET /api/state`;
  бенч `BenchmarkStaticsFromDomain` / `BenchmarkDestructiblesFromDomain`;
  фронт — `staticCombatMap` в `src/api.test.ts` (формат ключа `kind:id`).
- Ворота (TASK-110): `TestUnit_GateCombat_*` — endpoint в каждом секторе,
  зарядка щита, атака без hostility-оракула, гибель → sever графа + отказ
  роутера + reap второй стороны + персист, отказ прыжка, RAM-only без repo;
  `TestUnit_Topology_DestroyGate_RemovesTheLink`,
  `TestUnit_PathRouter_DestroyedGateInvalidatesCache`,
  `TestIntegration_World_DestroyedGateIsNotLoaded`.

## Отложено

- Персист HP/щитов статики между рестартами (RAM-only; разрушение
  персистится для башен/спутников/генераторов/ворот); race-hostility для
  NPC-статики; HP-бар на канвасе (только шит); tower-repair и ремонт ворот
  (TASK-67); дроп-лут со статики; killer-атрибуция (6.3).
- Ракеты по статике сделаны в TASK-113; ворота — TASK-110; живое состояние в
  welcome-кадре — TASK-186 (кроме возврата в big-radar окно, см. выше).

## Ссылки

- Источник: отложенные пункты `phase4-01/02/03/05/06`.
- Блокеры: `phase6-02-relations.md`, `phase6-02a-combat-hostility-wiring.md`.
