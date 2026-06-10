# Quest engine (phase 8.12)

Backend for the tutorial (and later missions): persisted per-player progress,
in-code quest definitions, a background poller that advances steps from
observable player state, and cash rewards. Promotes the client-only checklist
of 8.8 to server-tracked quests with rewards. Task:
`docs/tasks/phase8-12-quest-engine.md`.

## 1. Model

- Definitions in code (`internal/quest/defs.go`), not the DB — like balance.
- Progress in `player_quests(player_id, quest_id, step_index, status, state,
  started_at, completed_at)`, one row per (player, quest), `status` active →
  completed.
- A `Step` has a `Kind` + params + `RewardCash` + `Desc`. MVP kinds, all
  boolean over a polled `Snapshot{Docked, CargoUnits, Cash}`:
  - `dock` — the player's ship is docked.
  - `acquire_cargo{qty}` — hold has ≥ qty units.
  - `earn_cash{amount}` — cash ≥ amount.

## 2. Signals — the poller

The hard part is knowing when a step is done. The MVP uses a **reconciler
poller** (`Closer`, ~5 s), not new domain events (trade/dock/arrive emit
none). Each tick `ProcessAll` lists active quests and, per player, reads a
`PlayerState` snapshot (ship docked? hold units? cash?) and advances every
current step the snapshot already satisfies.

A single (conservatively stale) snapshot can clear several steps in one tick.
Rewards are **not** re-read into the snapshot, so a reward can never satisfy
the gate it just funded (e.g. the earn-cash step). Events (precise,
transactional actions) are a later upgrade.

## 3. Advance + reward (exactly once)

Each step advance runs in one transaction: grant `RewardCash`
(`players.AdjustCash`) **and** move `step_index` (or mark `completed` on the
last step) together. Because the step pointer moves in the same commit, a
step's reward is granted exactly once even across crashes/retries.

## 4. Start

Lazy: `GET /api/quests/active` starts the tutorial (`Ensure` at step 0) on the
first call and returns the active step. This avoids iterating all players or
touching registration; existing players pick it up on first fetch.

## 5. Starter tutorial

`dock` (+500) → `acquire_cargo{1}` (+500) → `earn_cash{15000}` (+5000, granted
after the gate passes). Start cash is 10000; the two early rewards (1000) keep
cash below the 15000 gate, so the player must actually trade to finish.

## 6. API / Frontend

- `GET /api/quests/active` → current quest + step + progress + reward + done.
- SPA quest panel shows the objective/progress (supersedes the 8.8 checklist
  for authenticated quests).

## 7. Deviations / deferred

- **Events over polling**: precise step types (`sold N`, `killed X`) need
  `trade.completed`/`docked`/`kill` events — not emitted yet. Poller covers
  the MVP tutorial.
- **Per-step counters** (`state` JSONB) unused by the boolean MVP steps.
- **Branching quests, repeatable missions, cargo/ship rewards** — later.
- **Per-player notifications** on completion would reuse the shared
  player-events primitive (see rent `OverdueTopic`, auction notifications).

## Phase 8.17 — v2 scripted framework

Extends the 8.12 poller into a hybrid event+poll engine.

**Step taxonomy v2** (`StepKind`): polled — `dock`/`acquire_cargo`/`earn_cash`
(8.12) + `goto_sector`/`dock_at` (enriched Snapshot: `CurrentSector`,
`DockedTarget`); event-driven — `kill`/`deliver`/`trade` (a counter accumulates
toward `Count`). `Step.MatchEvent(ev)` returns the per-event delta;
`Step.EventDriven()` tells the poller to skip and wait for `OnEvent`.

**Events.** `Service.OnEvent(ctx, Event)` reconciles a discrete signal against
the player's active quests, accumulating the current step's counter
(`state` JSONB `{"p":N}`) and advancing (reward) at the goal, each advance in a
`FOR UPDATE` tx so it serialises with the poller. Bus wiring (`app.go`): the
kill bus (`sector.EntityKilledEvent`) → `EventKill`; api-published
`quest.cargo.delivered` (ship→station unload) → `EventDeliver`;
`quest.trade.completed` (player buy/sell) → `EventTrade`.

**State machine.** Statuses `active`/`completed`/`failed`/`abandoned`. The
poller fails a quest whose `deadline_at` (set at accept = now + `Def.Deadline`)
has passed. Rewards stay exactly-once (granted in the advancing tx). Chains:
`Def.Prerequisite` — `Accept` rejects until the prerequisite is `completed`.

**API.** `GET /api/quests/active` now returns a list (counter, deadline,
failed). `GET /api/quests/offerable`, `POST /api/quests/{id}/accept|abandon`.
Frontend `QuestPanel` v2 lists active quests (step counter, deadline countdown,
status) + an "available quests" accept section + abandon.

**Migration** 0033 adds `player_quests.deadline_at`.

### 8.17 deferred (PARTIAL)
- **Quest-NPC spawn + `escort_survive`** (criterion 2): the heavy sector
  integration (a spawn command, gate-spawn, despawn lifecycle, escortee
  survival) is NOT implemented. `kill` works by count via the existing kill
  bus; a target-bound kill (`Step.Target`) is supported in the model but no
  quest spawns a linked NPC yet. → follow-up before 8.18's spawn quests.
- Demo offerable quests (`patrol`/`saga1`/`saga2`) exercise the framework; the
  13 X-Tension microquests are 8.18.
- NPC dialogs/job-board flavour, stealth, generalised per-player notifications.
