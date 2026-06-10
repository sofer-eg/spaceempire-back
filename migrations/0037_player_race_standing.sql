-- +goose Up
-- +goose StatementBegin
-- Phase 9.4: per-player reputation with each NPC race. The police of a race
-- (its main-race navy) drops the player's standing when it catches contraband
-- or when the player destroys its ships; standing <= a threshold makes the
-- player "wanted" and the race opens fire. Default standing is 0 — only
-- non-default rows are stored (absence means neutral). The fast lookup is kept
-- in RAM by the racestanding Service (Precount), mirroring relations (6.2);
-- this table is just persistence. See back/docs/specs/police_contraband.md.
CREATE TABLE player_race_standing (
    player_id  BIGINT      NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    race       SMALLINT    NOT NULL,
    standing   INT         NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (player_id, race)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE player_race_standing;
-- +goose StatementEnd
