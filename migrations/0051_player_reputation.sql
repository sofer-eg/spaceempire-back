-- +goose Up
-- +goose StatementBegin
-- Phase 10.3.3: player reputation model. StarWind kept a single war/trade
-- rating per account (users.warstatus / users.tradestatus); equipment ranks
-- gate installs against min_war_rate / min_trade_rate / min_race_rate. We mirror
-- that as three per-player integers (common, not per-race — per-race relations
-- already live in player_race_standing, 9.4). Default 0 keeps every existing
-- player at the lowest rank. The ResolveInstall rank gate (10.3.4) reads these;
-- accrual from kills/trades/race relations is wired incrementally.
ALTER TABLE players
    ADD COLUMN war_rate   INT NOT NULL DEFAULT 0,
    ADD COLUMN trade_rate INT NOT NULL DEFAULT 0,
    ADD COLUMN race_rate  INT NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE players
    DROP COLUMN war_rate,
    DROP COLUMN trade_rate,
    DROP COLUMN race_rate;
-- +goose StatementEnd
