-- +goose Up
-- +goose StatementBegin
-- Phase 10.3.14: drop the aggregate players.race_rate. There is no single
-- "race rating" — StarWind kept per-race relations (users.rel_argon/boron/...),
-- mirrored here by player_race_standing (9.4). The equipment rank gate now reads
-- the player's standing with the shipyard's race for min_race_rate, so the
-- aggregate column is dead. war_rate / trade_rate stay (real single ratings,
-- ports of users.warstatus / users.tradestatus).
ALTER TABLE players
    DROP COLUMN race_rate;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE players
    ADD COLUMN race_rate INT NOT NULL DEFAULT 0;
-- +goose StatementEnd
