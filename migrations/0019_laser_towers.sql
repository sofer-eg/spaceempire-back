-- +goose Up
-- +goose StatementBegin
-- Phase 4.5: stationary defensive laser towers (port of SP TO_LaserTower,
-- legacy table laser_towers / object_type 6). Read-only sector statics
-- this phase — towers fire at hostile ships but are not yet damageable
-- (that is 4.6 KillObject). The legacy mode/attack_npc/log targeting knobs
-- are deferred until relations (6.2). See back/docs/specs/lasertowers.md.
CREATE TABLE laser_towers (
    id        BIGSERIAL        PRIMARY KEY,
    owner_id  BIGINT           REFERENCES players(id) ON DELETE SET NULL,
    sector_id BIGINT           NOT NULL REFERENCES sectors(id),
    pos_x     DOUBLE PRECISION NOT NULL,
    pos_y     DOUBLE PRECISION NOT NULL,
    hp        INTEGER          NOT NULL DEFAULT 50000,
    shield    INTEGER          NOT NULL DEFAULT 50000,
    race      INTEGER          NOT NULL DEFAULT 0,
    built     BOOLEAN          NOT NULL DEFAULT TRUE
);

CREATE INDEX laser_towers_sector_id_idx ON laser_towers(sector_id);

-- Seed: one NPC tower in sector 1, near the trade station at (0,0), within
-- the SPA viewport (±200/±150). owner NULL = race-owned (SP owner=0).
INSERT INTO laser_towers (sector_id, pos_x, pos_y, race) VALUES
    (1, 60, -60, 1);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS laser_towers;
-- +goose StatementEnd
