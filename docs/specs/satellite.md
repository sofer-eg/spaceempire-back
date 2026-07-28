# Navigation satellite (phase 10.15)

A player-deployed static sector object. New `EntityKind` (`EntityKindSatellite`,
value 11) modelled on the laser tower (`back/internal/persistence/lasertowers`,
phase 4.5 / 6.2b). In the original StarWind a navigation satellite was a drone
subtype (`ct_drones`, class 5/6); here it is a first-class static, like every
other sector object.

## Object

`domain.Satellite{ID, OwnerID, SectorID, Pos, Race, Built, HP, Shield,
MaxShield, ShieldRecharge}`. Persisted in table `satellites`. Loaded once at
worker cold-start into `SectorStatics.Satellites` (so it survives a restart),
flattened into the per-sector `destructibles` map for combat exactly like a
tower.

## Three roles (per 10.15 brainstorm)

1. **Beacon** — rides the static-object path: rendered by the 10.13 `satellite`
   silhouette, clickable, labelled "Навигационный спутник".
2. **Destructible** — has HP/Shield/ShieldRecharge, takes laser damage, recharges
   its shield each tick, and is removed by `killStatic` on death. Destruction is
   persisted (`satellitesRepo.Delete`) so a restart does not resurrect it —
   mirrors the laser-tower persistence wiring (8.5).
3. **Sector radar reveal** — while a live satellite is present in a sector, a
   subscriber's AOI radius is boosted to `cfg.SatelliteRevealRadius` (default
   10000 = covers the whole ±5000 sector from any interior point). This expands
   both the ship AOI window and, via `× RadarBigMultiplier`, the big-object
   static window — so the player sees the whole sector on radar instead of just
   their own radar bubble. The reveal is **owner/clan-gated** (phase 10.20 L5):
   the boost applies only to a subscriber who owns a built satellite in the
   sector or is allied (clan/friend, per `Relations`) to an owner — an enemy's
   satellite does not light the sector for you. `satellitesPresent()` is the
   cheap once-per-tick gate; `satelliteRevealsFor(playerID, relations)` runs the
   per-subscriber owner/ally test (mirrors `hideStealthed`). An unowned
   satellite reveals to nobody. Cloaked-ship stealth rules still apply
   (`hideStealthed` runs after the boost).

## Spawn — player install command

There is no seed. A player deploys a satellite from a ship's cargo:

`POST /api/cmd/install-satellite {shipID}`. The handler (orchestrator) owns
**no cargo** — it only routes and maps:
1. Send `InstallSatelliteCommand{PlayerID, ShipID, GoodsType: 26}` to the sector
   worker. Goods id 26 is "Навигационный спутник" (`configs/balance.yaml`); the
   handler owns that constant so the sector package stays free of the goods
   catalog.
2. Wait for ack (`AckTimeout`) and map the outcome: `ErrShipNotFound` → 404,
   `ErrForbidden` → 403, `ErrShipDocked` → 400, `cargo.ErrInsufficientQuantity`
   → 400 "no satellite in cargo", `cargo.ErrGoodsTypeNotFound` → 500,
   `ErrInstallerUnavailable` → 503 "install unavailable: server misconfigured",
   ack timeout → 504 with **no compensation** (see atomicity below).

`InstallSatelliteCommand.apply` validates ownership and that the ship is not
docked — a rejected gate never touches the goods. Then `installSatellite` debits
the hold and creates the satellite through `sector.StaticInstaller` (app-side
adapter over `database.TxManager` + `cargo.ConsumeIn` +
`satellitesRepo.WithExecutor(tx).Create`), and only on success `addSatellite`
inserts it into `statics.Satellites` and the `destructibles` map at the ship's
current position. The new satellite reaches clients on the next tick via the
10.20 L2 `StaticsAdded` delta (it is in `destructibles`, hence in
`staticRefsInRadius`, and `collectStaticsByRefs` renders the full object).

`StaticInstaller` is the **only** install path: a worker built without one
refuses the command with `ErrInstallerUnavailable` → 503 rather than deploying a
free satellite, so `SatelliteRepo` carries only `Delete`.

### Atomicity of the install (TASK-144)

**Invariant: the goods debit and the satellite INSERT commit in ONE
transaction.** Either the transaction committed (player paid, row exists) or it
rolled back (neither); nothing enters the sector's RAM unless it committed.

This replaced a `Consume`-before-`Send` / `Refund`-on-failure orchestration in
the handler: since `AckTimeout` is only `TickInterval + 1s`, a delayed tick made
the handler refund and answer 504 while the command was still queued and applied
normally afterwards — goods returned, satellite built, free and repeatable. The
same hole (and the same fix) applies to `install-jammer`, where the object costs
≈ 1.13M cr; see `jammer.md` for the full write-up.

A 504 therefore means "outcome unknown", not "nothing happened", and the
install's DB call is bounded by `Config.RepoTimeout` (default 2 s) so a hung
Postgres cannot park the tick goroutine indefinitely. A whole inbox drain is
bounded too (`Worker.installBudget` ≈ one `RepoTimeout` of install DB time),
so a queue of installs cannot chain those stalls; the overflow applies on the
next wake-up.

`RepoTimeout` leaves one residual window, in a single direction: if the deadline
fires while `COMMIT` is in flight, Postgres commits while pgx reports
`DeadlineExceeded`, so the goods are charged and the `satellites` row exists but
`addSatellite` never ran — the satellite is inert (revealing nothing, invisible
to clients) until a restart's `LoadAll` reconciles it. Logged at ERROR
(`"static install outcome in doubt"`, with ship / sector / goods type); the
player sees 500. Full write-up in `jammer.md`.

Install HP/Shield are package constants (`satelliteHP` etc.) — these are deploy
defaults, not per-tick knobs, so they stay out of `Config`.

## Invariants

- Owner = the installing player; `OwnerID` drives the hostility gate (a
  satellite is attackable only by someone hostile to its owner — same oracle as
  towers/stations).
- `Built = true` always (installed satellites are immediately live).
- One writer per sector: `addSatellite` runs only inside the tick goroutine
  (command application), never from the HTTP handler.
