-- +goose Up
-- +goose StatementBegin
-- Phase 5.3: NPC ownership + trader identity.
--
-- All NPC ships belong to a single reserved system player "__npc__" so they
-- satisfy ships.player_id NOT NULL without making the column nullable.
-- password_hash '!' is not a valid bcrypt digest, so this row can never log
-- in. ON CONFLICT keeps the migration idempotent across re-runs.
INSERT INTO players (login, password_hash)
VALUES ('__npc__', '!')
ON CONFLICT (login) DO NOTHING;

-- npc_ships marks which ships are NPC fab-ships and links each to the home
-- station it serves (the factory it hauls from). A minimal port of the old
-- fab_npc_ships: the route, phase, and goods live in ai_state JSON, not here.
-- Used for idempotent spawn (skip a home already served) and for debug.
CREATE TABLE npc_ships (
    ship_id    BIGINT      PRIMARY KEY REFERENCES ships(id) ON DELETE CASCADE,
    home_kind  SMALLINT    NOT NULL,
    home_id    BIGINT      NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_npc_ships_home ON npc_ships (home_kind, home_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE npc_ships;
-- Removing the system player cascades to its ships (ships.player_id ON DELETE
-- CASCADE) and their ai_state rows — a clean teardown of all NPC data.
DELETE FROM players WHERE login = '__npc__';
-- +goose StatementEnd
