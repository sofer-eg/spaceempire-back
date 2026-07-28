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
   → 400 "no jammer in cargo", `cargo.ErrGoodsTypeNotFound` → 500,
   `ErrInstallerUnavailable` → 503 "install unavailable: server misconfigured",
   ack timeout → 504 with **no compensation** (see atomicity below).

`InstallJammerCommand.apply` validates ownership (`ErrForbidden`) and that the
ship is not docked (`ErrShipDocked`) — a rejected gate never touches the goods.
Then `installJammer` debits the hold and creates the generator through
`sector.StaticInstaller` (app-side adapter over `database.TxManager` +
`cargo.ConsumeIn` + `jammersRepo.WithExecutor(tx).Create`), and only on success
`addJammer` inserts it into `statics.Jammers` and the `destructibles` map at the
ship's current position. The new generator reaches clients on the next tick via
the 10.20 L2 `StaticsAdded` delta (it is in `destructibles`, hence in
`visibleStaticRefs`, and `collectStaticsByRefs` renders the full object).

`StaticInstaller` is the **only** install path. A worker built without one
(`WithStaticInstaller` lost in a refactor, a second pool-assembly site) refuses
the command with `ErrInstallerUnavailable` → 503 instead of creating the object:
the installer is what makes the player pay, so its absence must break loudly
rather than deploy ≈1.13M cr generators for free. Consequently `JammerRepo`
carries only `Delete` — installs never touch it.

### Atomicity of the install (TASK-144)

**Invariant: the goods debit and the generator INSERT commit in ONE
transaction.** Either the transaction committed (player paid, row exists) or it
rolled back (neither). Nothing is added to the sector's RAM unless the
transaction committed. One residual gap is described below.

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
  abandoned `buf=1` channel drops the result, but goods and generator went the
  same way.
- The install's DB call is bounded by `Config.RepoTimeout` (default 2 s) instead
  of an uninterruptible background context, so a hung Postgres stalls the tick
  for at most that long instead of forever.
- ONE drain of the worker's inbox spends at most ~`RepoTimeout` of DB time on
  synchronous writes (`Worker.dbBudget`, shared with the launch commands since
  TASK-147): `RepoTimeout` bounds a single install, so
  without a per-drain budget a queue of installs against a hung Postgres would
  park the Run goroutine — and every sector that worker owns — with no tick in
  between. Any player can fill that queue, because an install with an empty hold
  now reaches the worker (nothing is checked in the HTTP goroutine any more).
  Commands over the budget stay in the inbox and apply on the next tick /
  wake-up; their ack becomes a 504, which is safe under this invariant. The
  budget only decides whether the drain continues — it never shortens a
  command's own deadline, so a legal install is never failed by a spurious
  `DeadlineExceeded`.
- **What the budget does not do: bound the total.** `Run` resets it on every
  wake-up (`applyAndDrain`), and the overflow is still in the inbox, so the queue
  is worked through at ~`RepoTimeout` per install either way: the degradation
  window stays `InboxCapacity × RepoTimeout` (256 × 2 s ≈ 8.5 min) and a command
  queued behind it still waits that long for its ack. Measured on a worker with
  `TickInterval` 100 ms / `RepoTimeout` 50 ms, 20 installs against a hung DB and
  an `AddShipCommand` last: the add-ship ack still arrives after ~1.005 s
  (= queued × `RepoTimeout`, as before the fix) and the installer is still called
  20 times, but the sector ticks 4–8 times in that window (10 nominal) instead of
  exactly once, which is what the same measurement gives with the budget check
  disabled. So the win is "sectors keep ticking at a reduced rate" rather than
  "the stalls cannot chain". Bounding the total — moving the install off the tick
  goroutine — is still open.
- TASK-148 extended both halves of this contract to **every** DB and bus call the
  worker makes from its Run goroutine (pickups, hauls, hacks, captures, saves,
  deletes, publishes, the periodic batches), so `RepoTimeout` now bounds all of
  them and all of them charge the same drain budget. The install path is unchanged
  — it is where the discipline came from. See
  [`tick_db_timeouts.md`](tick_db_timeouts.md) for the per-call policy, including
  what an ambiguous deadline is taken to mean on the cargo-moving paths.

#### Residual window: in-doubt commit

`RepoTimeout` bounds the call, so it can also fire while `COMMIT` is already in
flight. pgx then tears the connection down and returns
`context.DeadlineExceeded` while Postgres commits anyway. The outcome is
one-directional: **goods charged + `jammers` row with `built=true`, but no
generator in RAM** — `addJammer` never ran, so it jams no jumps and no client
sees it. It is not a free object and not lost goods; it is an object that is paid
for but inert until the next restart, whose `LoadAll` seeds it into
`SectorStatics.Jammers` and reconciles the two. The player sees 500.

The window is small (it needs the deadline to land inside the commit) and is
logged at ERROR — `"static install outcome in doubt"` with ship, player, sector,
goods type and the error — so it can be reconciled by hand. Before `RepoTimeout`
existed the window was practically absent (`Create` ran under
`context.Background()`), but the trade was an unbounded tick stall; the bounded
stall is worth the narrow in-doubt case.

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
