-- +goose Up
-- +goose StatementBegin
-- Phase 4.6: loot containers (port of SP object_type 8). A container is a
-- pickup-able cargo drop left by a destroyed ship. Its cargo lives in the
-- cargo table under owner_kind = 8 (EntityKindContainer); this table holds
-- only the spatial/lifecycle fields. Persistent (immediate writes) but
-- immutable once created — see back/docs/specs/kill_object.md §6.
CREATE TABLE containers (
    id         BIGSERIAL        PRIMARY KEY,
    sector_id  BIGINT           NOT NULL,
    pos_x      DOUBLE PRECISION NOT NULL DEFAULT 0,
    pos_y      DOUBLE PRECISION NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ      NOT NULL,
    created_at TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_containers_sector ON containers (sector_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Container cargo rows (owner_kind = 8) would dangle once the table is gone,
-- so drop them in the same down-migration.
DELETE FROM cargo WHERE owner_kind = 8;
DROP TABLE containers;
-- +goose StatementEnd
