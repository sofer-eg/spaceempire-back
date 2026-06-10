# Bounties (phase 6.3)

Port of the old StarWind SP `TO_SetBounties` + the bounty-payout branch of
`KillObject`. The original is **race-driven** (NPC races auto-generate
bounties every 48h from `race_relations.Standing < 0`, and the killer is
resolved from the `war_rate` damage ledger). Phase 6.3 reinterprets this as a
**player-driven** feature, per `docs/tasks/phase6-03-bounties.md`: players (and
clans) place a price on another player's (or clan's) head; whoever lands the
killing blow collects.

## 1. Entities

A bounty is a contract:

| Field        | Meaning                                                    |
|--------------|------------------------------------------------------------|
| `Target`     | `EntityRef` — who is hunted. Kind ∈ {player, clan}.        |
| `Sponsor`    | `EntityRef` — who placed (and funded) it. Kind ∈ {player, clan}. |
| `Amount`     | Reward in credits, debited from the sponsor at set time.   |
| `Status`     | `active` → `paid` (claimed) or `expired` (timed out).      |
| `ExpiresAt`  | When an unclaimed bounty auto-expires and is refunded.     |
| `PaidTo`/`PaidAt` | The player credited on a payout (audit/history).      |

Funds are **escrowed**: the sponsor pays up front, so a bounty always has the
money behind it. On payout the killer is credited; on expiry the sponsor is
refunded. Money is never created or destroyed.

## 2. Killer attribution (prerequisite from 4.6)

The 4.6 kill sweep deletes a ship at `HP<=0` and emits `entity_killed`, but
deliberately **did not** track who fired the last shot (see `kill_object.md`
§2). Bounties need it. Minimal RAM-only addition:

- `domain.Ship.LastAttacker domain.PlayerID` (0 = none). Not persisted —
  combat state, like `AttackTarget`.
- Set at **every** site that applies damage to a ship, to the attacking
  player:
  - laser ship→ship (`fireLasers`): attacker's `PlayerID`
  - missile hit (`tickMissiles`): the missile's `PlayerID`
  - drone fire (`tickDrones`): the drone's `PlayerID`
  - laser tower (`tickTowers`): the tower owner's `PlayerID` (towers with no
    owner — NPC seed — leave it untouched)
- The sweep reads `ship.LastAttacker` and puts it in `EntityKilledEvent.Killer`
  alongside the new `VictimPlayer` (the dead ship's owner — needed because the
  ship row is gone by the time a consumer reacts).

`EntityKilledEvent` gains two fields: `Killer PlayerID`, `VictimPlayer
PlayerID`. Both default 0; existing static-kill paths leave them 0.

## 3. Payout (on `entity_killed`)

The bounty module subscribes to `entity_killed` on the same bus the sector
worker publishes to. On a ship victim:

1. Guard: `Killer != 0 && VictimPlayer != 0 && Killer != VictimPlayer`
   (the "own kill is not paid" rule from the task). NPC killers are not
   special-cased — see §7.
2. Resolve the victim's targets: `[playerRef(VictimPlayer)]`, plus
   `clanRef(victimClan)` if the victim belongs to a clan.
3. In one transaction (`SELECT … FOR UPDATE` on the matching active
   bounties): for each still-`active` bounty whose target is in the set,
   credit the killer `+= Amount` and mark it `paid` (`paid_to`, `paid_at`).

Multiple bounties on the same victim (e.g. one on the player, one on their
clan, or several sponsors) all pay out — the killer collects the sum, mirroring
the old SP's `full_bounty_sum`.

## 4. Set a bounty

`Service.SetBounty(ctx, caller, target, amount, ttl, fromClan)`:

- Validate: `amount > 0`, target kind ∈ {player, clan}, target ≠ sponsor.
- Sponsor: `fromClan=false` → the caller (player, own cash). `fromClan=true`
  → the caller's clan, and the caller must be its **leader** (6.1 only mints
  leader/member; officer spending is deferred).
- In one transaction: debit the sponsor wallet (`players.cash` or
  `clans.treasury_cash`) by `amount`; on insufficient funds abort with
  `ErrInsufficientFunds`; insert the bounty `active` with
  `expires_at = now + ttl`.

## 5. Expiry + refund

A background `Closer` (mirrors the auction closer) polls every `interval`:

1. `DueExpired(now, batch)` — active bounties with `expires_at <= now`,
   `FOR UPDATE`.
2. Per bounty, in a transaction: refund the sponsor wallet `+= amount`, mark
   `expired`.

The payout tx and the expiry tx both take `FOR UPDATE` on the bounty row, so a
bounty claimed in the same instant it expires is settled exactly once
(whichever tx wins the lock; the loser sees `status != active` and skips).

## 6. Read endpoints

- `GET /api/bounties` — top active bounties (status `active`,
  `expires_at > now`), highest `amount` first, with resolved target/sponsor
  display names (LEFT JOIN players/clans).
- `POST /api/bounties` — place a bounty (auth: caller = sponsor or clan
  leader).
- `GET /api/players/{id}/bounty-history` — every bounty ever targeting that
  player, any status, newest first.

## 7. Deviations / deferred

- **Race-driven generation** (the literal `TO_SetBounties` cron) is **not**
  ported — the new model is player-driven. NPC races do not place bounties.
- **NPC killer**: if a bounty target is killed by an NPC ship, the NPC's
  system player is the killer and the bounty pays out to that account. Cheap
  and harmless (the NPC account is invisible); a "real-player-only" guard is
  deferred — the task only specifies the own-kill exclusion.
- **Clan-target split**: the old SP pre-splits a clan bounty into per-member
  rows. We instead store one clan-target row and match it against the victim's
  current clan at kill time — membership changes are handled naturally, no
  pre-expansion.
- **Officer spending**: only the clan leader may fund a clan bounty (6.1 has
  no officer role yet).
