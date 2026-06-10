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

`POST /api/cmd/install-satellite {shipID}`. The handler (orchestrator, mirrors
`launch-missile`):
1. `Consume` 1× goods id 26 ("Навигационный спутник",
   `configs/balance.yaml`) from the ship's hold (Postgres transaction).
2. Send `InstallSatelliteCommand` to the sector worker, wait for ack.
3. On worker rejection or timeout — `Refund` the goods and propagate the error.

`InstallSatelliteCommand.apply` validates ownership and that the ship is not
docked, then `installSatellite`: `satellitesRepo.Create` (DB-assigned id;
fallback counter when no repo, for pure unit tests) → `addSatellite` inserts it
into `statics.Satellites` and the `destructibles` map at the ship's current
position. The new satellite reaches clients on the next tick via the 10.20 L2
`StaticsAdded` delta (it is in `destructibles`, hence in `staticRefsInRadius`,
and `collectStaticsByRefs` renders the full object).

Install HP/Shield are package constants (`satelliteHP` etc.) — these are deploy
defaults, not per-tick knobs, so they stay out of `Config`.

## Invariants

- Owner = the installing player; `OwnerID` drives the hostility gate (a
  satellite is attackable only by someone hostile to its owner — same oracle as
  towers/stations).
- `Built = true` always (installed satellites are immediately live).
- One writer per sector: `addSatellite` runs only inside the tick goroutine
  (command application), never from the HTTP handler.
