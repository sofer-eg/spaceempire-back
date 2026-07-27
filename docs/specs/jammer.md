# Hyper-interference generator / Генератор гипер-помех (TASK-131)

A player-deployed static sector object that jams the seamless jump drive
(`up_jump_drive`) of every ship near it. New `EntityKind`
(`EntityKindJammer`, value 13) modelled 1:1 on the navigation satellite
(`back/docs/specs/satellite.md`, phase 10.15).

## SP original

`ct_drones` row `(125, class=7, 'Генератор гипер-помех', speed=0,
acceleration=0, laser=0, fire_range=0, shield=4000, hull=3560,
maneureability=10, model=5, cargo_id=27, ttl=80640)`.

`model=5` is the family of stationary deployables: class 6 = navigation
satellite, class 7 = this generator, class 8 = hyper-beacon. Class 6 was already
ported as a first-class static rather than a drone, so class 7 follows the same
route — spaceempire drones are mobile, carrier-bound, TTL 120 s, recallable, and
a long-lived stationary object does not fit them.

The jam itself is the second branch of `SP DoJump`'s hyper-interference gate
(`starwind/sql/db.sql:12127-12133`), run after the `up_antijump` ship scan:

```sql
if(catch_id = 0) then
  select id into catch_id from drones where class=7 and sector=object_sector limit 1;
end if;
...
if(catch_id != 0) then
  set message = '...Обнаружены помехи неизвестного происхождения в гипер-поле. Прыжок невозможен.';
  leave DoJump;
end if;
```

## Object

`domain.Jammer{ID, OwnerID, SectorID, Pos, Race, Built, HP, Shield, MaxShield,
ShieldRecharge}`. Persisted in table `jammers` (migration `0061_jammers.sql`).
Loaded once at worker cold-start into `SectorStatics.Jammers` (so it survives a
restart), flattened into the per-sector `destructibles` map for combat exactly
like a satellite.

Deploy defaults are package constants next to the install code
(`internal/sector/jammer.go`), mirroring the table column defaults:

| Field            | Value | Origin                                                        |
|------------------|-------|---------------------------------------------------------------|
| `HP`             | 7500  | satellite 5000 × SP hull ratio 3560/2420 ≈ 1.47               |
| `Shield`         | 4000  | SP `ct_drones` class 7 `shield` verbatim                       |
| `MaxShield`      | 4000  | same                                                           |
| `ShieldRecharge` | 20    | same cadence as satellite / laser tower                        |

The SP `ttl = 80640` ticks is **not** ported (neither was the satellite's): the
generator lives until it is destroyed.

## Two roles

1. **Destructible static** — HP/Shield/ShieldRecharge, takes laser/missile/
   torpedo damage (`IsStaticTargetKind` includes `EntityKindJammer`), recharges
   its shield each tick, and is removed by `killStatic` on death. Destruction is
   persisted (`jammersRepo.Delete`) so a restart does not resurrect it. Shooting
   it down is the only way to lift its zone.
2. **No-jump zone** — `Worker.jammerActive` in
   `internal/sector/jumpdrive.go`.

## The jump gate

`JumpDriveCommand.apply` calls `w.antijumpActive(s, ship) || w.jammerActive(s,
ship)` at gate 9 — **before** paying the shield and stamping the cooldown, so a
blocked jump costs the player nothing. Both branches report the shared sentinel
`ErrJumpBlockedByAntijump` (same SP gate, same player-visible outcome), which
`internal/api/jump_drive.go` maps to **HTTP 409**.

`jammerActive` scans `s.statics.Jammers`:

- **Own sector only.** The scan runs on the jumper's own `sectorState`, porting
  SP's `sector = object_sector`. Jumping *into* a jammed sector is allowed, as
  in the original. (SP also warned — but did not block — on a gate transit into
  such a sector, `db.sql:11707`; that warning is out of scope.)
- **No owner filter.** Faithful SP: the class-7 query has no `object_owner`
  predicate, so the generator jams its owner and their allies exactly as it jams
  everyone else. There is deliberately no relations oracle in this gate.
- **`Built` only.** A destroyed generator has already left `statics.Jammers` via
  `killStatic`/`removeStaticFromLayout`, so the zone lifts the same tick.
- **Radius `Config.JammerRange`, default 2000**, Euclidean
  (`Pos.Sub().Length()`), uniform with `AntijumpRange`/`MineRange`/
  `CaptureRange`. This is the **one deliberate departure from SP**: the original
  blanketed the entire sector with no radius at all, which at the ±5000
  half-extent is a blunt instrument; a circle keeps it a positional play and
  matches the ship-carried `up_antijump` field.

## Spawn — player install command

There is no seed. A player deploys a generator from a ship's cargo:

`POST /api/cmd/install-jammer {shipID}`. The handler (orchestrator, mirrors
`install-satellite`):
1. `Consume` 1× goods id 27 ("Генератор гипер-помех", `configs/balance.yaml`,
   avg_price 1 134 216) from the ship's hold (Postgres transaction).
2. Send `InstallJammerCommand` to the sector worker, wait for ack.
3. On worker rejection or timeout — `Refund` the goods and propagate the error.

`InstallJammerCommand.apply` validates ownership (`ErrForbidden`) and that the
ship is not docked (`ErrShipDocked`), then `installJammer`: `jammersRepo.Create`
(DB-assigned id; fallback counter when no repo, for pure unit tests) →
`addJammer` inserts it into `statics.Jammers` and the `destructibles` map at the
ship's current position. The new generator reaches clients on the next tick via
the 10.20 L2 `StaticsAdded` delta (it is in `destructibles`, hence in
`staticRefsInRadius`, and `collectStaticsByRefs` renders the full object).

## Not a satellite

The generator does **not** reveal the sector radar — that is satellite-only
(10.15 / 10.20 L5). It has no `Config.*RevealRadius` and no
`jammersPresent`/`jammerRevealsFor` analogue.

## Invariants

- Owner = the installing player; `OwnerID` drives the **combat** hostility gate
  (attackable only by someone hostile to its owner — same oracle as
  towers/satellites/stations). It does **not** gate the jam.
- `Built = true` always (installed generators are immediately live).
- One writer per sector: `addJammer` runs only inside the tick goroutine
  (command application), never from the HTTP handler.
- New static kind wired in all three places (TASK-113 regression): the
  `domain.SectorStatics.Jammers` field, `SectorStatics.IsEmpty()`, and
  `cloneStatics()` in `worker.go` — missing the last one silently drops the
  object from the WS welcome snapshot.

## UI

- `CombatHUD` «Развёртывание»: «Установить генератор помех» with the hold count
  (goods 27), disabled at zero.
- Canvas glyph `JammerGlyph` (amber emitter core + radiating arcs), pickable,
  with a shield bar; matching row icon in `objectIcons`.
- `TargetsPanel` → «Другое» tab, labelled «Генератор гипер-помех».
- `ObjectActionsMenu`: not dockable (like the satellite/tower); «Лететь» and the
  weapon actions stay.
