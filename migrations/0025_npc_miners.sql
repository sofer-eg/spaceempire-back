-- +goose Up
-- +goose StatementBegin
-- Phase 5.4: minable asteroids + NPC miner identity.

-- Asteroids are minable ore bodies at a fixed position in a sector. NPC
-- miners drill them: mass falls, the row is deleted when depleted. ore_type
-- is the goods type the asteroid yields, stored directly (no "asteroid type
-- -> goods" mapping). density/type from the old schema are dropped — the
-- miner logic never used them.
CREATE TABLE asteroids (
    id         BIGSERIAL        PRIMARY KEY,
    sector_id  BIGINT           NOT NULL REFERENCES sectors(id),
    pos_x      DOUBLE PRECISION NOT NULL,
    pos_y      DOUBLE PRECISION NOT NULL,
    mass       BIGINT           NOT NULL,
    ore_type   BIGINT           NOT NULL,
    updated_at TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);

CREATE INDEX asteroids_sector_id_idx ON asteroids(sector_id);

-- Seed: two iron (goods type 2 = Железо) asteroids in sector 1, near the
-- microchip factory (station id=1 at 100,100) whose recipe consumes iron.
-- Positions stay clear of the seed statics (trade station 0,0; shipyard
-- -180,-80) and the gates (±900).
INSERT INTO asteroids (sector_id, pos_x, pos_y, mass, ore_type) VALUES
    (1,  60, 140, 200, 2),
    (1, 150,  60, 200, 2);

-- npc_ships gains a controller_kind discriminator so the cold-start spawner
-- counts traders and miners on the same home independently (a factory can
-- have both). Existing rows are traders.
ALTER TABLE npc_ships
    ADD COLUMN controller_kind TEXT NOT NULL DEFAULT 'trader';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE npc_ships DROP COLUMN IF EXISTS controller_kind;
DROP TABLE IF EXISTS asteroids;
-- +goose StatementEnd
