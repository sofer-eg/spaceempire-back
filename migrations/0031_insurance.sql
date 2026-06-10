-- +goose Up
-- +goose StatementBegin
-- Phase 6.5: ship insurance. A player buys a policy on a ship; if the ship is
-- destroyed while the policy is active and unexpired, the holder is paid
-- `coverage` (status → claimed). Premium is non-refundable on expiry. At most
-- one active policy per ship (partial-unique). See
-- back/docs/specs/insurance.md.
CREATE TABLE insurance_policies (
    id           BIGSERIAL   PRIMARY KEY,
    ship_id      BIGINT      NOT NULL,
    player_id    BIGINT      NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    premium_paid BIGINT      NOT NULL CHECK (premium_paid > 0),
    coverage     BIGINT      NOT NULL CHECK (coverage > 0),
    status       TEXT        NOT NULL DEFAULT 'active',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL,
    claimed_at   TIMESTAMPTZ
);

-- One active policy per ship; payout lookup.
CREATE UNIQUE INDEX idx_insurance_active_ship ON insurance_policies (ship_id) WHERE status = 'active';
CREATE INDEX idx_insurance_player ON insurance_policies (player_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE insurance_policies;
-- +goose StatementEnd
