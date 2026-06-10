-- +goose Up
-- +goose StatementBegin
-- Phase 6.3: player/clan-driven bounties. A sponsor (player or clan) escrows
-- `amount` credits on a target's (player or clan) head; the killer of the
-- target collects on a claim, the sponsor is refunded on expiry. target_kind/
-- sponsor_kind are domain.EntityKind values (9 = player, 10 = clan), the same
-- typed-ref encoding the relations table uses. See
-- back/docs/specs/bounties.md.
CREATE TABLE bounties (
    id           BIGSERIAL   PRIMARY KEY,
    target_kind  SMALLINT    NOT NULL,
    target_id    BIGINT      NOT NULL,
    sponsor_kind SMALLINT    NOT NULL,
    sponsor_id   BIGINT      NOT NULL,
    amount       BIGINT      NOT NULL CHECK (amount > 0),
    status       TEXT        NOT NULL DEFAULT 'active',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL,
    paid_to      BIGINT,
    paid_at      TIMESTAMPTZ
);

-- Payout lookup at kill time: active bounties on a given target.
CREATE INDEX idx_bounties_active_target ON bounties (target_kind, target_id) WHERE status = 'active';
-- Expiry sweep: active bounties ordered by their deadline.
CREATE INDEX idx_bounties_active_expiry ON bounties (expires_at) WHERE status = 'active';
-- History endpoint: every bounty ever targeting a player.
CREATE INDEX idx_bounties_target ON bounties (target_kind, target_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE bounties;
-- +goose StatementEnd
