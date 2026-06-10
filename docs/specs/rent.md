# Rent (phase 6.4)

Reinterpretation of SP `TO_RentCheck`. The original charges **cargo-storage
rent** (`count · avg_price · arenda_ppd · elapsed/144400`) for goods parked in
NPC/player stations, deleting the cargo on non-payment. Phase 6.4 reinterprets
this as **ownership upkeep**, per `docs/tasks/phase6-04-rent.md`: a player owes
periodic rent on each station-like object they own; non-payment past a limit
confiscates the object (owner cleared to NPC/gov).

## 1. Model

`rents(id, payer_id, station_kind, station_id, amount_per_period,
unpaid_periods, last_paid_at, next_due_at, created_at)`, `UNIQUE
(station_kind, station_id)` — one rent per object. `station_kind` is a
`domain.EntityKind` (2=station, 3=shipyard, 4=trade_station). Rent is a credit
**sink** — debited from the payer, paid to nobody (the "gov").

## 2. Reconcile (auto-create)

There is no station-acquisition feature yet, so nothing populates `rents`
directly. `Service.Reconcile` bridges the gap: it lists every player-owned
station/shipyard/trade-station (`stations.PlayerOwned`) and `Ensure`s a rent
row (idempotent `INSERT … ON CONFLICT DO NOTHING`) with the configured
`DefaultAmount` and `next_due_at = now + Period`. Run at startup and on each
billing tick, so a newly owned object starts owing rent without a restart. A
future "buy station" feature can call `Ensure` directly for an exact price.

## 3. Billing (`ProcessDue`)

Every tick (`Closer`, ~1h) processes rents with `next_due_at <= now`
(`FOR UPDATE SKIP LOCKED`), all in one transaction:

- **Charge** `amount_per_period` from the payer's cash.
  - success → `MarkPaid` (clear `unpaid_periods`, advance `next_due_at`).
  - `ErrInsufficientCash` → `unpaid_periods++`:
    - below `MaxUnpaid` → `MarkUnpaid` (store count, advance `next_due_at` so
      the next cycle retries), emit a warning `OverdueEvent`.
    - at/above `MaxUnpaid` → **confiscate**: `stations.ClearOwner` (owner_id
      → NULL) + delete the rent row, emit a confiscation `OverdueEvent`.

`OverdueEvent`s are published **after** the transaction commits, to the
payer's WS topic (`rent.overdue.<playerID>`), and forwarded to the client as a
`rent_overdue` frame.

`ErrInsufficientCash` comes from a `UPDATE … WHERE cash+delta>=0 RETURNING`
that affects 0 rows (pgx no-rows, not a SQL error), so the transaction stays
usable and the loop continues with the next rent.

## 4. Config

- `Period` (default 24h) — billing cycle / `next_due_at` advance.
- `MaxUnpaid` (default 3) — missed charges before confiscation.
- `DefaultAmount` (default 5000) — per-period rent for auto-reconciled rows.
- Closer interval (default 1h) — how often due rents are scanned.

## 5. Deviations / deferred

- **Cargo-storage rent** (the literal SP) is not ported — replaced by
  ownership upkeep.
- **No upstream creator**: rents come only from reconcile (player-owned
  objects) until a station-acquisition feature lands and calls `Ensure` with a
  real price. With the current NPC-only seeds, `rents` is empty in practice.
- **RAM ownership staleness**: confiscation clears `owner_id` in Postgres, but
  a running sector worker keeps the object's old owner in RAM until restart
  (same RAM-only caveat as 6.2b). A runtime owner-refresh is deferred.
- **No frontend** (task scope is back+db). The server emits the `rent_overdue`
  WS frame; a SPA consumer is a later task.
- **Integration tests** (testcontainers) are not run locally — task 7.6.
