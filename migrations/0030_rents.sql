-- +goose Up
-- +goose StatementBegin
-- Phase 6.4: ownership rent. A player owes periodic upkeep on each station-
-- like object they own; non-payment past max_unpaid periods confiscates it
-- (owner_id cleared to NULL = NPC/gov). station_kind is a domain.EntityKind
-- (2=station, 3=shipyard, 4=trade_station). One rent row per object
-- (UNIQUE) so the auto-reconcile EnsureRent is idempotent. See
-- back/docs/specs/rent.md.
CREATE TABLE rents (
    id                BIGSERIAL   PRIMARY KEY,
    payer_id          BIGINT      NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    station_kind      SMALLINT    NOT NULL,
    station_id        BIGINT      NOT NULL,
    amount_per_period BIGINT      NOT NULL CHECK (amount_per_period >= 0),
    unpaid_periods    SMALLINT    NOT NULL DEFAULT 0,
    last_paid_at      TIMESTAMPTZ,
    next_due_at       TIMESTAMPTZ NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (station_kind, station_id)
);

CREATE INDEX idx_rents_payer ON rents (payer_id);
CREATE INDEX idx_rents_due ON rents (next_due_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE rents;
-- +goose StatementEnd
