-- +goose Up
-- +goose StatementBegin
-- Phase 10: persistent torpedoes (TASK-100.3.5, ЧТЗ doc-1 §3 FR-001).
-- A torpedo is a persistent combat object with HP and a TTL (modelled on
-- the drones table) carrying the homing physics of a missile plus a splash
-- profile. Like drones, torpedoes survive a restart, so they live in their
-- own table. Ammunition goods (gt23/gt24) already exist from migration
-- 0042 — no goods_types row is added here (ЧТЗ §7 C-03).
CREATE TABLE torpedos (
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
    last_target_x DOUBLE PRECISION NOT NULL DEFAULT 0,
    last_target_y DOUBLE PRECISION NOT NULL DEFAULT 0,
    class         SMALLINT         NOT NULL,
    damage        INTEGER          NOT NULL,
    speed         DOUBLE PRECISION NOT NULL,
    accel         DOUBLE PRECISION NOT NULL,
    turn_rate     DOUBLE PRECISION NOT NULL,
    hit_radius    DOUBLE PRECISION NOT NULL,
    splash_radius DOUBLE PRECISION NOT NULL,
    hp            INTEGER          NOT NULL,
    expires_at    TIMESTAMPTZ      NOT NULL,
    updated_at    TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_torpedos_sector ON torpedos (sector_id);
CREATE INDEX idx_torpedos_owner_ship ON torpedos (owner_ship_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE torpedos;
-- +goose StatementEnd
