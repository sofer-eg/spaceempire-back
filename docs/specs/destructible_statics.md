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
- Ракеты по статике сделаны в TASK-113; ворота — TASK-110.

## Ссылки

- Источник: отложенные пункты `phase4-01/02/03/05/06`.
- Блокеры: `phase6-02-relations.md`, `phase6-02a-combat-hostility-wiring.md`.
