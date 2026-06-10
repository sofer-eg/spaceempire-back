# Quest-NPC spawn lifecycle (phase 8.18 vertical slice)

Closes the deferred "критерий 2" of 8.17: a quest can spawn NPCs, track their
death/survival, and despawn them on a terminal transition. Built on the 9.5
runtime spawn (`sector.AddShipCommand` with a controller) plus a new despawn
command. Implements 3 representative quests; the remaining 10 are a later
increment.

## Spawn / despawn

- `sector.RemoveShipCommand{ShipID}` — the despawn counterpart of AddShipCommand:
  drops the ship from RAM (ships / controllers / dirty / cooldown) and deletes
  its DB row (ai_state cascades). Idempotent (a missing ship is a no-op — it
  may have already been killed).
- `app.questSpawner` implements `quest.Spawner` over the 9.5 warship machinery
  (`buildWarship` + `ships.Create` + `ai_state` + AddShipCommand): a quest NPC
  is a race ship (system-owned, race controller) — Xenon/pirate enemies, a
  main-race escortee. `FromGate` spawns at a gate exit into the sector (siege),
  else at the sector centre. Despawn finds each ship's live sector via
  `pool.LookupShipSector` and sends RemoveShipCommand.
- Quest NPCs carry no `npc_ships` row (like invasion ships); they reload as
  race NPCs on restart and the quest's `state.Spawned` keeps referencing them.

## Quest engine extensions

- `Def.Spawns []QuestSpawn` — specs resolved on Accept: spawn each, record
  `role → []shipID` in `state.Spawned`.
- `Step.TargetRole` — binds a kill/escort step to the spawned ships of a role
  (instead of a static `Target` ref). A `kill` step's goal becomes the number
  of spawned ships of that role; the event counts only for those victims.
- `StepEscortSurvive` — polled survival timer: the escortee must survive
  `Count` poller ticks (counter in `state.Progress`). The escortee's death
  (`EventKill` whose victim is the escortee) **fails** the quest.
- Despawn on every terminal transition (complete / fail / abandon) removes the
  quest's still-living spawned NPCs (post-commit, best-effort).

## Quests (slice)

| ID | Flavour | Steps | Spawn |
|----|---------|-------|-------|
| 6008000 | Убить беглого убийцу | goto_sector{N} → kill{role=target} | 1 pirate "target" in N |
| 6002100 | Небольшая осада | goto_sector{N} → kill{role=enemy} | Xenon group from a gate into N |
| 6002300 | Атакованный торговец | escort_survive{role=escortee, Count ticks} | 1 main-race escortee + pirate wave in N |

All Offerable (accept/abandon from the board); rewards granted once on the
final step, none on fail. Targets adapted to the numeric world (sectors chosen
at wiring; original names kept as flavour).
