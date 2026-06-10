# Police, contraband & wanted (phase 9.4)

New mechanic from X-Tension canon — **not** in the original StarWind. The
navy of the main races (1–5) acts as police: it scans player ships inside its
own sectors for contraband, confiscates it, drops the player's standing with
that race, and once the player is "wanted" the navy opens fire.

## Data

- **Illegal goods per race** — `internal/reference/contraband`, Go literal
  (data is tiny, like the 8.13 race reference). MVP: Slaves (goods 323, seeded
  in 5.6) are illegal for races 1–5 and legal for pirates (6). Drugs/space-fuel
  are deferred (Slaves is the only canonical contraband currently in the
  catalog; "Космическое топливо" is a normal trade good, not contraband).
- **Player standing per race** — table `player_race_standing(player_id BIGINT,
  race SMALLINT, standing INT, PK(player_id,race))`, start 0. Service with a
  RAM cache mirroring `social/relations` (6.2): `Precount` / `Get` / `Adjust` /
  `IsWanted` / `Decay`. Wanted ⇔ `standing ≤ WantedThreshold` (default −10).

## Police identity (deviation from design)

The design suggested a dedicated `controller_kind='police'`. We simplify
(Karpathy #2): **any navy ship of a main race (1–5) is police**. Identification
in the worker is `ship.Race ∈ PoliceRaces`, and the scan only targets ships
with **no AI controller** (real players — every NPC has an ai_state controller,
players have none). A per-target scan cooldown bounds the frequency, so making
all navy scan does not multiply DB load. A distinct police role/patrol is a
follow-up.

## Scan & enforcement (sector tick)

`tickPoliceScan` runs after `fireLasers`. For each police-race ship, for each
real-player ship within `ScanRange` not on cooldown, it calls the injected
`PoliceScanner.Scan` (app-side, over `cargo.Service` + the standing service):

1. Read the player's hold (`cargo.Inventory`).
2. Any good illegal to the police race → **confiscate** it (`cargo.Consume`,
   immediate-persist / atomic) and drop standing by `ContrabandPenalty`.
3. Return whether contraband was found + whether the player is now wanted.

The worker sets a cooldown for the scanned ship and, when contraband was found,
publishes a per-player `PoliceScanEvent` (`police.scan.<playerID>` bus topic →
WS frame → journal). A clean scan emits nothing (no spam).

## Standing changes

- **Caught with contraband**: −`ContrabandPenalty` (default 5).
- **Destroying a faction ship**: when a navy ship (race 1–5) is killed and its
  `LastAttacker` is a real player, the worker calls
  `PoliceScanner.OnRaceShipKilled` → −`KillPenalty` (default 10). (Hooked in
  the kill sweep, reading `LastAttacker` — the same attribution bounties use.)
- **Decay toward neutral**: a periodic `racestanding.Closer` nudges every
  standing one `DecayStep` (default 1) toward 0 each interval (default 1h).
  Racial missions (8.18) are a later, faster recovery path.

## Targeter overlay (wanted → hostile)

`wantedOverlayTargeter` wraps the 9.1 `raceMatrixTargeter`: a main-race ship
(1–5) is hostile to a real-player ship (race 0, not the NPC owner) when that
player is wanted with the ship's race. So the navy (and thus police) engages a
wanted player on top of the default race matrix. Player↔player and NPC↔NPC
hostility are unchanged.

## Frontend

A reputation panel lists the main races with the player's standing and a
"WANTED" badge; police scan/confiscation events land in the combat journal.
