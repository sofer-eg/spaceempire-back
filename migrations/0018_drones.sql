-- +goose Up
-- +goose StatementBegin
-- Phase 4.4: persistent combat drones (port of SP TO_Drones model=1).
-- Unlike missiles (reconstructable, RAM only) drones survive a restart,
-- so they live in their own table. See back/docs/specs/drones.md §3.
CREATE TABLE drones (
    id            BIGSERIAL        PRIMARY KEY,
    sector_id     BIGINT           NOT NULL,
    owner_ship_id BIGINT           NOT NULL REFERENCES ships(id) ON DELETE CASCADE,
    player_id     BIGINT           NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    pos_x         DOUBLE PRECISION NOT NULL DEFAULT 0,
    pos_y         DOUBLE PRECISION NOT NULL DEFAULT 0,
    vel_x         DOUBLE PRECISION NOT NULL DEFAULT 0,
    vel_y         DOUBLE PRECISION NOT NULL DEFAULT 0,
    direction_x   DOUBLE PRECISION NOT NULL DEFAULT 1,
    direction_y   DOUBLE PRECISION NOT NULL DEFAULT 0,
    target_kind   SMALLINT         NOT NULL,
    target_id     BIGINT           NOT NULL,
    hp            INTEGER          NOT NULL,
    damage        INTEGER          NOT NULL,
    expires_at    TIMESTAMPTZ      NOT NULL,
    updated_at    TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_drones_sector ON drones (sector_id);
CREATE INDEX idx_drones_owner_ship ON drones (owner_ship_id);
-- +goose StatementEnd

-- +goose StatementBegin
-- Each launched drone costs one Combat Drone cargo unit. Id chosen above
-- the missile seed (50) so future trade-good imports keep contiguous
-- numbering. space=2 mirrors the missile's cargo footprint.
INSERT INTO goods_types (id, name, space) VALUES (51, 'Combat Drone', 2);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM goods_types WHERE id = 51;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE drones;
-- +goose StatementEnd
