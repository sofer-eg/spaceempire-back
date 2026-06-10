# Laser Towers — port of SP `TO_LaserTower` (phase 4.5)

Source: `starwind/sql/db.sql` procedure `TO_LaserTower` (object_type 6,
table `laser_towers`).

## 0. Scope of phase 4.5

Stationary defensive towers that automatically damage hostile ships in
range each tick. This phase ports the **acquisition + fire loop** and the
**persistence/rendering plumbing**. Two parts of the original SP are
intentionally out of scope:

- **Real hostility.** The whole SP target filter is built on relations
  (`race_relations`, `tmp_tbl_hostility_*`, tower `mode` 0–4). Relations
  arrive in **phase 6.2**. Until then the production wiring uses
  `combat.NoHostility` — a stub that considers nobody hostile, so towers
  load and tick but never fire. The fire path is exercised by tests with
  an injected owner-based predicate. When 6.2 lands, only the predicate
  changes.
- **Towers as damage targets.** Nothing damages a tower yet (ships only
  attack ship targets — see lasers.md). Tower destruction + container
  drops are **phase 4.6** (`KillObject`). Because a tower never loses HP
  this phase, its SP shield gate (`shield/base_shield > 0.3`) is always
  satisfied and is not modelled; towers are stored as read-only sector
  statics, not mutable per-tick state.

## 1. Domain

`domain.LaserTower` (in `SectorStatics.LaserTowers`):

| field    | meaning                                            |
|----------|----------------------------------------------------|
| ID       | DB primary key (`LaserTowerID`)                    |
| OwnerID  | `*PlayerID`, nil for NPC/race towers (SP owner=0)  |
| SectorID | sector the tower sits in                           |
| Pos      | fixed world position                               |
| HP       | hull (display + future 4.6 destruction)            |
| Shield   | shield (display + future)                          |
| Race     | owning race (display)                              |
| Built    | construction flag (always true for seed)           |

`EntityKindLaserTower = 7`. (The PHP packed object_type was 6, but our
`EntityKind` enum already assigned 6 to drones; the enum is independent of
the legacy packed id per `starwind/CLAUDE.md`.)

The SP per-row knobs `mode`, `attack_npc`, `log` are hostility-targeting
controls — omitted until 6.2 needs them. Per-shot `Damage` / `Range` are
SP procedure constants, not per-row data, so they live in `TowerSpec`, not
the struct.

## 2. Acquisition + fire (`combat` package)

`TowerSpec{ Range float64; Damage int }`, `DefaultTowerSpec()` calibrated
to the current tight seed scale (statics at ±100..180 units): `Range=150`,
`Damage=20`. (SP uses `ltw_range=300`, `ltw_good_range=150`,
`ltw_laser=25000`; 25000 is a one-shot kill against the original 6-figure
HP and does not translate to our ~100-HP starter ships — recalibrated.)

`HostilePredicate func(towerOwner *PlayerID, ship *Ship) bool`.
`NoHostility` returns false (production stub).

`SelectTowerTarget(t LaserTower, ships, spec, hostile) *Ship` returns the
**nearest hostile ship within `spec.Range`**, skipping `HP<=0`. This
collapses the SP's four-tier target selection (shot distribution across
many towers sharing one target) to nearest-hostile: that distribution is
a multi-tower load optimisation and is deferred until multi-tower density
matters. Single-tower behaviour is identical (it picks the closest valid
target).

Fire = `combat.ApplyDamage(target, spec.Damage)` (shield first, then HP),
done by the sector tick.

## 3. Sector integration

`Worker.tickTowers(s)` runs in the combat cluster of `tickSector` (after
`fireLasers`). For each tower in `s.statics.LaserTowers` it selects a
target via the worker's `hostile` predicate and, if found, applies damage
and marks the target dirty. The predicate is a worker field defaulting to
`combat.NoHostility`, overridable in tests via `WithHostility`.

No per-tick visual beam is emitted: with the production stub no shot ever
fires, so a tower-beam WS effect + SPA render would be untestable
speculation. It is deferred to 6.2 (real fire) / 4.7 (combat HUD).

## 4. Persistence

`internal/persistence/lasertowers` repo: `LoadAll(sector)` (cold-start
seed of `SectorStatics.LaserTowers`), `Create` (returns DB id — for the
future build path and integration tests), `Delete` (immediate write for
the future 4.6 destruction path). Towers do not mutate this phase, so
there is no `BatchUpdate`.

## 5. Tests

- `combat`: nearest-hostile selection, out-of-range excluded, dead ship
  skipped, `NoHostility` selects nothing, own ship not attacked.
- `sector`: tower with an injected owner-based predicate damages a foreign
  ship in range and not its owner's ship; `NoHostility` leaves all ships
  untouched.
- `persistence` (testcontainers): LoadAll round-trips a seeded tower;
  Create returns id; Delete removes it.

## 6. Acceptance criteria mapping

- "Башни автономно защищают свою территорию" → tickTowers + injected-
  predicate sector test (production needs 6.2 for a non-stub predicate).
- "Может разрушиться и восстановиться через ремонт станции" → deferred:
  tower damage/destruction is 4.6, repair-via-station is not in the old
  SP, so it is left for a later balance pass.
