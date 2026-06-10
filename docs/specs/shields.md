# Спецификация: Щиты (phase 4.1)

Порт SP `TO_ShipShieldCharge` из старой StarWind
(`/home/sof/projects/go/src/starwind/sql/db.sql:33866-34010`).
SP `TO_ObjectShieldCharge` (статика — gates / shipyards / trade_stations /
stations, `db.sql:29707-29850`) в этой задаче НЕ портируется: фаза 4.1
скоуп ограничен щитами кораблей; статические объекты получат свой
combat-цикл в фазе 4.5 (Laser Towers) и/или последующей доводке.

## Источник в старом StarWind

`TO_ShipShieldCharge` каждый тик игры обходит таблицу `ships` курсором и
обновляет колонку `shield`:

```sql
-- курсор берёт ships с shield<max_shield (или с hide+shield>0)
fetch sc_ships into ship_id, owner, shield, max_shield, shield_charge,
                    up_shield, hide_status;

if (up_shield = 0) then
    if (shield > 0) set shield = 0;       -- щит без апгрейда «срывается»
    iterate;
end if;

if (hide_status) then
    set shield = shield - shield_charge / 2;   -- стелс тратит щит
else
    set shield = shield + shield_charge;       -- обычный заряд
end if;

if (shield < 0)         set shield = 0;
if (shield > max_shield) set shield = max_shield;

update ships set shield = ship_shield where ID = ship_id;
```

Дополнительно в финале SP удаляются строки `war_rate` (метки атакующих)
для целей, у которых щит достиг `max_shield`. Эта часть относится к
hostility/relations (фаза 6.2) и в текущую задачу не входит.

## Что не портируем (упрощения)

| Сущность | Старая StarWind | Новая spaceempire | Причина |
|---|---|---|---|
| `updates.up_shield` уровень апгрейда | гейтит зарядку, при `up_shield=0` обнуляет щит | поле `MaxShield int` на корабле, при `MaxShield=0` щита нет | у нас нет таблицы `updates`, апгрейды решим иначе позже |
| `updates.up_hide` стелс | при `hide=1` тратит `shield_charge/2` за тик | не реализуем | стелс-механика — отдельная задача, вне фазы 4 |
| `war_rate` очистка | удаление меток при полном щите | не делаем | hostility — фаза 6.2 |
| SP `TO_ObjectShieldCharge` (статика) | заряжает shield у gates/shipyards/ts/stations | не делаем | фаза 4.5 / отдельная задача |

## 1. Domain-модель

### Ship (расширение)

```go
type Ship struct {
    // ...
    HP             int    // текущий HP (уже было)
    MaxHP          int    // потолок HP (новое)
    Shield         int    // текущий щит (уже было)
    MaxShield      int    // потолок щита (новое; 0 = щита нет)
    ShieldRecharge int    // приращение щита за один тик (новое)
}
```

**`ShieldRecharge int`, не float.** Соответствует `ships.shield_charge`
старой схемы (целочисленное приращение за вызов SP). Дробное накопление
было бы полезно при дробных recharge per second, но у нас фиксированный
тик и нет потребителей дробных значений — Karpathy #2 (Simplicity First).

**Дефолты:** `MaxHP=100, MaxShield=100, ShieldRecharge=1`. Совпадает с
дефолтом `hp/shield` из миграции `0001_initial_schema.sql`. Существующие
корабли в БД получат эти значения через миграцию.

### Что НЕ кладём на корабль на этой фазе

- Энергию (`energy/max_energy/energy_recharge`) — нужна для лазеров (4.2).
- Damage / range / weapon stats — на оружие, не на корабль.
- Class id и upgrade levels — балансовый каталог классов появится позже.

## 2. Чисто-доменный API: `internal/combat`

Новый пакет без БД-зависимостей. Все функции мутируют переданный
`*domain.Ship`; вызывает их sector worker внутри своего тика — таким
образом сохраняется one-writer-per-sector инвариант.

### `combat/shields.go`

```go
// ChargeShield увеличивает Shield корабля на ShieldRecharge единиц за
// тик, не превышая MaxShield. Возвращает true, если значение Shield
// изменилось — sector worker по этому флагу метит корабль dirty для
// периодического сохранения.
//
// Корабли с MaxShield==0 (нет щитового модуля) пропускаются: Shield
// остаётся 0, возвращается false.
//
// Корабли с Shield>=MaxShield пропускаются (уже full): возвращается
// false.
//
// Корабли с Shield>MaxShield (рассинхрон после изменения класса)
// зажимаются в MaxShield, возвращается true.
func ChargeShield(ship *domain.Ship) bool
```

### `combat/damage.go`

```go
// Damageable — то, к чему можно применить ApplyDamage. На фазе 4.1
// единственная реализация — *domain.Ship; в дальнейшем добавятся
// станции, дроны, лазерные башни.
type Damageable interface {
    TakeDamage(dmg int) DamageResult
}

// DamageResult описывает поглощение урона: сколько ушло в щит, сколько
// в HP, остался ли цел объект. Sector worker по полю Killed принимает
// решение об удалении (фаза 4.6).
type DamageResult struct {
    ShieldAbsorbed int   // 0..min(dmg, Shield)
    HPAbsorbed     int   // 0..min(dmg-ShieldAbsorbed, HP)
    Overkill       int   // остаток после HP=0; пока никому не нужен, но фиксируем
    Killed         bool  // true когда HP==0 после применения
}

// ApplyDamage кладёт урон target'у: сначала в щит до 0, остаток в HP.
// dmg<=0 — no-op (возвращает zero-value, target не трогается).
func ApplyDamage(target Damageable, dmg int) DamageResult
```

`*domain.Ship` будет имплементировать `TakeDamage` в пакете `domain`
(чтобы избежать импорт-цикла `combat → domain → combat`).

## 3. Интеграция в sector tick

В функции `sector.tickSector` появляется combat-фаза между движением и
production:

```go
func (w *Worker) tickSector(ctx context.Context, s *sectorState, dt float64) {
    started := w.clock.Now()
    resolveAutopilot(s, w.router, w.cfg.DockRange)
    applyMovement(s, dt)
    w.tryAutoJump(s)
    chargeShields(s)           // ← новый шаг 4.1
    w.runProduction(ctx, s, started)
    w.persistDirty(ctx, s)
    // ...
}

func chargeShields(s *sectorState) {
    for id, ship := range s.ships {
        if combat.ChargeShield(ship) {
            s.markDirty(id)
        }
    }
}
```

**Почему между tryAutoJump и production.** Production не зависит от щитов;
порядок «движение → бой → экономика» совпадает с интуицией старой игры
(SP вызывались последовательно `TO_ShipMovement` → `TO_ShipShieldCharge`
→ `To_Production`). Будущие фазы вставят `applyLasers`, `tickMissiles`,
`tickDrones` рядом с `chargeShields`.

**Damaged-корабли тоже заряжаются.** SP делает то же самое — он не
проверяет, что корабль не в бою. Если корабль одновременно получает
урон и заряжает щит, эффективный recharge = `ShieldRecharge - damage`.
Это явное балансное свойство.

## 4. Персистентность

- **Shield/HP** — обычные dirty-флаги, попадают в periodic snapshot раз
  в 5 секунд (см. фазу 1.3). `BatchUpdate` уже пишет `hp/shield` —
  ничего менять не надо.
- **MaxHP/MaxShield/ShieldRecharge** — class-characteristics. Пишутся
  один раз при `Create`, потом не обновляются (как `MaxSpeed/
  Acceleration/TurnRate`).
- **Смерть** (HP=0) — immediate-write в фазе 4.6. На фазе 4.1 при HP=0
  worker оставляет корабль в state без действий — фаза 4.6 добавит
  `KillObject` обработчик.

## 5. Схема БД

Миграция `back/migrations/0015_ship_combat.sql`:

```sql
-- +goose Up
-- +goose StatementBegin
ALTER TABLE ships
    ADD COLUMN max_hp          INTEGER NOT NULL DEFAULT 100,
    ADD COLUMN max_shield      INTEGER NOT NULL DEFAULT 100,
    ADD COLUMN shield_recharge INTEGER NOT NULL DEFAULT 1;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ships
    DROP COLUMN shield_recharge,
    DROP COLUMN max_shield,
    DROP COLUMN max_hp;
-- +goose StatementEnd
```

`Repository.LoadAll`, `Create` подхватывают новые колонки; `Save` и
`BatchUpdate` их НЕ обновляют (class-fixed после Create).

## 6. Frontend

UI-задача отдельная — 4.7 (combat HUD). На 4.1 фронт не трогается:
`Shield`/`HP` уже пробрасываются в WS-патче, поле `MaxShield` появится в
доменной модели и автоматически попадёт в JSON через cloning Snapshot
→ patch builder. Конкретные шкалы здоровья и щита — задача 4.7.

## 7. Тесты

### Unit (combat package)

| Тест | Сетап | Ожидание |
|---|---|---|
| `TestUnit_ChargeShield_BelowMax` | `Shield=50, MaxShield=100, ShieldRecharge=10` | `Shield=60`, return `true` |
| `TestUnit_ChargeShield_AtMax` | `Shield=100, MaxShield=100, ShieldRecharge=10` | `Shield=100`, return `false` |
| `TestUnit_ChargeShield_Clamps` | `Shield=95, MaxShield=100, ShieldRecharge=10` | `Shield=100`, return `true` |
| `TestUnit_ChargeShield_NoShieldModule` | `MaxShield=0, Shield=0, ShieldRecharge=10` | `Shield=0`, return `false` |
| `TestUnit_ChargeShield_AboveMaxClampsDown` | `Shield=150, MaxShield=100` | `Shield=100`, return `true` |
| `TestUnit_ApplyDamage_ShieldOnly` | `Shield=100, HP=50, dmg=40` | `Shield=60, HP=50, ShieldAbsorbed=40, HPAbsorbed=0, Killed=false` |
| `TestUnit_ApplyDamage_ShieldPlusHP` | `Shield=30, HP=100, dmg=50` | `Shield=0, HP=80, ShieldAbsorbed=30, HPAbsorbed=20, Killed=false` |
| `TestUnit_ApplyDamage_KillsShip` | `Shield=0, HP=20, dmg=50` | `Shield=0, HP=0, HPAbsorbed=20, Overkill=30, Killed=true` |
| `TestUnit_ApplyDamage_Zero` | `Shield=100, HP=50, dmg=0` | без изменений, zero DamageResult |
| `TestUnit_ApplyDamage_Negative` | `Shield=100, HP=50, dmg=-10` | без изменений (не лечим) |

### Sector tick

| Тест | Сетап | Ожидание |
|---|---|---|
| `TestUnit_Tick_ChargesShields` | один корабль `Shield=50, MaxShield=100, ShieldRecharge=5`, 3 тика | `Shield=65`, ship в dirty-set |
| `TestUnit_Tick_DoesNotChargeFullShield` | `Shield=100, MaxShield=100` | dirty-set пустой |

### Acceptance паритет (опционально)

Не реализуем на 4.1: оригинал SP завязан на `updates` и `hide_status`,
паритет без портирования этих систем будет искусственным. Если в фазе 6
появятся стелс и upgrades — вернёмся к acceptance-тесту.

## 8. Критерии приёмки (из задачи)

- [x] Щит игрока виден в UI (`Shield`/`MaxShield` уже в snapshot)
- [x] Щит регенерирует (combat phase в tickSector)
- [x] Damage сначала уходит в щит, потом в HP (`ApplyDamage`)
- [x] При HP=0 корабль помечается «убит» (`DamageResult.Killed`); собственно
      удаление — фаза 4.6
