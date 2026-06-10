# Insurance (phase 6.5)

Reinterpretation of the old `insure.php`: a player insures a ship; if it is
destroyed while covered, they are paid out. Per
`docs/tasks/phase6-05-insurance.md`.

## 1. Model

`insurance_policies(id, ship_id, player_id, premium_paid, coverage, status,
created_at, expires_at, claimed_at)`. Status: `active` → `claimed` (paid out)
or `expired` (lapsed). At most one **active** policy per ship (partial-unique
index `(ship_id) WHERE status='active'`). Premium is the player's up-front
stake; `coverage = premium × CoverageMultiplier` (default 10) is the payout.

## 2. Buy

`Service.Buy(ctx, player, shipID, premium, durationDays)`:

- Validate `premium > 0`, `durationDays > 0` (capped to `MaxDurationDays`,
  default 90).
- Authorize against `ships`: the caller must **own** the ship and it must be
  **docked** (commerce at a station, consistent with trade / auction 3.21).
- In one transaction: lazy-expire any time-lapsed active policy on the ship
  (so the unique index does not block a re-insure), debit the premium
  (`ErrInsufficientFunds` on shortfall), insert the active policy
  (`coverage = premium × multiplier`, `expires_at = now + durationDays`).
  A genuinely-active (unexpired) policy makes the insert hit the unique index
  → `ErrAlreadyInsured` and the whole tx rolls back (premium refunded).

## 3. Payout (on `entity_killed`)

The Service subscribes to `entity_killed` on the same bus bounties use (a
second subscriber). On a ship victim it runs `OnKill(shipID)`: in one tx, find
the ship's active **unexpired** policy (`FOR UPDATE`), and if present credit
the holder `coverage` and mark it `claimed`. An expired policy is not returned
by the lookup, so a lapsed ship is not paid (the task's edge case).

## 4. Read

`GET /api/insurance` lists the caller's policies. A row still `active` in the
DB but past `expires_at` is reported as `expired` (lazy expiry is only flushed
on re-insure — see §2), so the UI shows the true state.

## 5. Frontend

A new "Страховка" tab in the docked StationView (3.8): shows the current
ship's active policy + expiry, and a buy form (premium + duration). Coverage is
previewed as premium × multiplier.

## 6. Deviations / deferred

- **No expiry sweep**: expiry is lazy (flushed on re-insure) + filtered at
  payout/read. A background sweep is unnecessary for correctness; premiums are
  non-refundable on lapse (real insurance), so nothing is owed back.
- **Coverage is a fixed multiple of premium** rather than a player-chosen sum
  (the task's Buy signature takes premium, not coverage).
- **Integration tests** (testcontainers) are not run locally — task 7.6.
