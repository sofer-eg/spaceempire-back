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
(дружественная/нейтральная статика неуязвима). Ворота не таргетятся.

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

## Покрытие тестами

- `TestUnit_StaticCombat_DamagesHostileStation` — шит→HP.
- `TestUnit_StaticCombat_DestroysHostileStation` — удаление из combat-набора и
  рендер-слоя.
- `TestUnit_StaticCombat_FriendlyInvulnerable` — гейт hostility.
- `TestUnit_StaticCombat_ShieldRecharges` — зарядка + clamp.
- Миграция `0028` верифицирована на PG16.

## Отложено

- Персист combat-состояния статики (RAM-only); ракеты/дроны vs статика
  (только лазер); ворота; race-hostility для NPC-статики; HP-бар на канвасе
  (только шит); tower-repair; дроп-лут со статики; killer-атрибуция (6.3).

## Ссылки

- Источник: отложенные пункты `phase4-01/02/03/05/06`.
- Блокеры: `phase6-02-relations.md`, `phase6-02a-combat-hostility-wiring.md`.
