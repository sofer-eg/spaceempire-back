# Spec: KillObject + container drops (port of SP `KillObject`)

Phase 4.6. Source: `starwind/sql/db.sql` — `KillObject`, `create_container`,
`create_drop`; `starwind/sql/npc_passenger.sql` — `drop_slaves_on_kill`.

## 1. Scope vs the original SP

The original `KillObject(object_id, object_type)` is a ~1100-line monster
that dispatches on `object_type` (0 user, 1 gate, 2 shipyard, 3 trade
station, 4 station, 5 ship, 6 laser tower, 7 drone, 8 container, 9
missile, 11 asteroid) and, for ships, also: ejects docked pilots, drops
cargo into containers, pays/clears bounties, recomputes race relations
and war-status, respawns the owner at their home shipyard, fires
insurance, logs the kill, and writes `messages_sys` notifications.

Phase 4.6 ports **only what a ship death needs in the current engine**:

- **Only ships die in 4.6.** `Ship` is the only `Damageable`
  (`combat.ApplyDamage`); drones/missiles/towers/stations are not damaged
  by the current combat loop, so the kill handler only fires for ships
  whose HP reached 0. Drone self-destruct already has its own path
  (`removeDrone`, phase 4.4) and drones carry no cargo, so they are not
  routed through here.
- **Cargo → containers** with the SP's drop mechanics (see §3).
- **`entity_killed` bus event** for future consumers (AI 5.x, bounties
  6.3, stats 7.1). Emitted best-effort; payload carries the victim ref,
  sector and position.
- **Immediate, transactional DB write**: delete the ship, delete its
  leftover cargo, create the drop containers — all in one transaction
  (a kill is a critical event, like trade/jump; §4).

**Explicitly NOT ported in 4.6** (deferred, each to its own phase):

- bounties / race relations / war-status — phase 6.2 / 6.3,
- owner respawn at home shipyard, insurance — later phases,
- `messages_sys` player notifications — no in-game mail yet,
- docked-pilot ejection / docked-ship ejection — no carrier/inside-ship
  docking model yet,
- `create_drop` from `ct_drop_list` (NPC loot tables) — phase 5.x,
- **slaves drop** (`drop_slaves_on_kill`) — phase **5.6**. Go ships have
  no `passengers` field yet (passenger NPCs arrive in 5.5), so there is
  nothing to drop. The kill handler is the single drop path 5.6 will
  extend; see §5.
- gate / station / asteroid destruction (`object_type` 1–4, 11) — those
  objects are not damaged by the 4.x combat loop.

## 2. Trigger: kill sweep

The damage sites (`fireLasers`, `tickTowers`, `tickMissiles`,
`tickDrones`) already drive a ship's HP to 0 and clear the attacker's
`AttackTarget` when `DamageResult.Killed` fires; phase 4.1–4.5 left the
corpse in `sectorState.ships` with a `// 4.6 kill handler` TODO.

4.6 adds **one sweep step** to `tickSector`, after all combat phases and
before persistence: `sweepKilledShips` walks `s.ships`, and for every
ship with `HP <= 0` calls `killShip`. A sweep (rather than killing at
each of the four damage sites) is chosen because:

- the four sites already skip dead targets (`target.HP > 0` guards), so a
  ship reaches the sweep dead exactly once;
- it keeps attribution and cargo I/O in one place instead of four;
- it matches the existing `removeDrone` corpse-removal shape.

Killer attribution is **not** tracked in 4.6 (the sweep does not know
which of the four sources landed the final blow). The `entity_killed`
event therefore omits the killer; when bounties (6.3) need it, the damage
sites will stamp a `LastAttacker` ref on the ship.

## 3. Cargo drop mechanics (faithful port)

The SP runs two cargo loops over the dead ship's stacks (`cargo` rows
with `location = ship_full_id`):

**Missile stacks** (`cargo_missiles` cursor — cargo whose goods type is a
missile; in Go that is goods type id `50`, seeded by `0017_missile_goods`):

```
chance = round(16 * rand())          # 0..16
if chance < 12:        destroy stack, no drop      # ~72%
throw = count >> chance              # chance ∈ 12..16
if throw < 5:          destroy stack, no drop
else:                  drop `throw` units into a container
```

**Every other stack** (`cargo` cursor): the full stack always drops —
one container per stack, positioned at the ship's death point with a
`±20` unit random jitter.

So a destroyed freighter with N non-missile stacks leaves **N
containers** (one per goods type); missile stacks usually burn up.

`combat.PlanShipDrops(items, missileType, rng) []combat.Drop` is the pure
port of both loops (`rng.Float64()` stands in for `rand()`); it returns
one `Drop{GoodsType, Quantity}` per surviving stack. The worker turns
each `Drop` into a `domain.ContainerDrop` by attaching the jittered
position and `expiresAt = now + ContainerTTL`, then hands the slice to
the persistence layer.

## 4. Persistence (immediate, transactional)

`create_container` in the SP is `INSERT containers` + `INSERT cargo
(owner=0, location=(container<<4)+8)`. In Go a container is a cargo owner
like any other: its cargo rows are `cargo` rows with
`owner_kind = EntityKindContainer (8)`, `owner_id = container.id`. No new
cargo plumbing is needed — `ListByOwner` / `UsedSpace` already work for
kind 8; only `Capacity` does not (containers have no `cargobay` column),
which is fine because nothing adds to a container after creation.

`persistence/containers.Repository` owns the compound writes inside a
single `TxManager` transaction:

- **`RecordKill(ctx, victim, sectorID, drops)`** → `[]domain.Container`:
  for each drop INSERT a container + its cargo row; then
  `DELETE FROM cargo WHERE owner = ship` (leftover, undropped cargo) and
  `DELETE FROM ships WHERE id = victim`. A ship with no surviving drops
  still deletes cleanly and returns zero containers — that is the
  "ship without cargo → no container" acceptance case.
- **`Pickup(ctx, container, ship)`**: list the container's cargo,
  capacity-check the **ship** (`Capacity`/`UsedSpace`), `Add` every stack
  to the ship, then delete the container's cargo + the container. All or
  nothing — if the ship cannot fit the whole container, `ErrNoSpace` and
  nothing moves.
- **`Delete(ctx, id)`**: TTL expiry — delete the container's cargo + the
  container row.
- **`LoadAll(ctx, sectorID)`**: cold-start, mirrors the drones repo.

The worker calls these synchronously from the tick (immediate write,
like `droneRepo.Create/Delete`). A nil repo (pure unit tests) disables
persistence: the ship is still removed from RAM and the event still
fires, but nothing is written and no container is created.

## 5. Slaves drop (phase 5.6 — implemented)

`drop_slaves_on_kill` reads `ships.passengers`, computes
`floor(passengers * rand(20..50) / 100)` and drops that many goods-323
("Рабы") units. Phase 5.5 added `Ship.Passengers` and passenger NPCs, so
5.6 wired this into the existing drop path: `combat.PlanSlavesDrop(passengers,
rng)` returns the count (one `rng.Float64` consumed), and `dropLoot` appends a
`Drop{GoodsType: 323}` to the plan when `ship.Passengers > 0` — it then rides
the same ring layout + `RecordKill` container path as any cargo stack. No
change to the sweep or transport code, as anticipated. The only buyers are
pirate bases (§ trade: a buy-only goods-323 `station_goods` row per pirbase,
migration 0027) — selling reuses the normal dock→market→sell flow.

## 6. Container lifecycle & transport

- **Domain**: `Container{ ID ContainerID; SectorID; Pos Vec2; ExpiresAt
  time.Time }`; `EntityKindContainer = 8`. Persistent (immediate
  writes), but **immutable** once created — cargo inside only changes via
  pickup, which removes the whole container. So containers need no
  dirty-tracking and no periodic `BatchUpdate` (unlike drones).
- **TTL**: `tickContainers` deletes (RAM + immediate DB) any container
  past `ExpiresAt`. `Config.ContainerTTL` default 600 s.
- **Pickup proximity**: `PickupContainerCommand` requires the ship within
  `Config.PickupRange` (default 30 u) of the container — "достаточно
  близко", looser than `DockRange` (3 u) because a container is not a
  dockable object.
- **WS**: containers ride the per-tick AOI diff exactly like drones, but
  with only an added/removed delta (immutable ⇒ no "updated"). A
  freshly-subscribed client receives every visible container in
  `containersAdded` on its first patch. `dto.Container{ id, x, y }`.
- **HTTP**: `POST /api/cmd/pickup-container {shipID, containerID}`.

## 7. Acceptance

- ship with cargo killed → one container per surviving stack with that
  cargo (`combat.PlanShipDrops` + `RecordKill`);
- ship without cargo → no container, ship gone;
- container visible in the sector radar (WS `containersAdded`);
- pickup transfers cargo to the ship and removes the container;
- SPA renders containers on the canvas and offers "Подобрать".
