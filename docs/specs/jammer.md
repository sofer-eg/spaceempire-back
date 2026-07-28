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
`install-satellite`) owns **no cargo** — it only routes and maps:
1. Send `InstallJammerCommand{PlayerID, ShipID, GoodsType: 27}` to the sector
   worker. Goods id 27 is "Генератор гипер-помех" (`configs/balance.yaml`,
   avg_price 1 134 216); the handler owns that constant so the sector package
   stays free of the goods catalog.
2. Wait for ack (`AckTimeout`) and map the outcome: `ErrShipNotFound` → 404,
   `ErrForbidden` → 403, `ErrShipDocked` → 400, `cargo.ErrInsufficientQuantity`
   → 400 "no jammer in cargo", `cargo.ErrGoodsTypeNotFound` → 500, ack timeout
   → 504 with **no compensation** (see atomicity below).

`InstallJammerCommand.apply` validates ownership (`ErrForbidden`) and that the
ship is not docked (`ErrShipDocked`) — a rejected gate never touches the goods.
Then `installJammer` debits the hold and creates the generator through
`sector.StaticInstaller` (app-side adapter over `database.TxManager` +
`cargo.ConsumeIn` + `jammersRepo.WithExecutor(tx).Create`), and only on success
`addJammer` inserts it into `statics.Jammers` and the `destructibles` map at the
ship's current position. The new generator reaches clients on the next tick via
the 10.20 L2 `StaticsAdded` delta (it is in `destructibles`, hence in
`visibleStaticRefs`, and `collectStaticsByRefs` renders the full object).

With no installer wired the command falls back to a bare `jammersRepo.Create`
(or a fallback id counter) and accounts for no goods — that path exists only for
pure unit tests.

### Atomicity of the install (TASK-144)

**Invariant: the goods debit and the generator INSERT commit in ONE
transaction.** Either the player paid and the generator exists, or neither
happened. Nothing is added to the sector's RAM unless the transaction committed.

This replaced a `Consume`-before-`Send` / `Refund`-on-failure orchestration in
the handler, which leaked a free generator: `AckTimeout` is only
`TickInterval + 1s`, so a delayed tick made the handler refund and answer 504
while the command was still in the inbox and applied normally a moment later
— goods returned, generator built, repeatable at will (≈ 1.13M cr each).

Consequences of the new invariant:
- A 504 means "outcome unknown", not "nothing happened": the player checks the
  hold and retries only if no generator appeared. A retry on an empty hold is
  refused with 400.
- The lost ack itself is harmless — `replyInstallJammer` writing into an
  abandoned `buf=1` channel drops the result, but the world and the hold already
  agree.
- The install's DB call is bounded by `Config.RepoTimeout` (default 2 s) instead
  of an uninterruptible background context, so a hung Postgres stalls the tick
  for at most that long instead of forever.

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
