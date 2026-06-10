-- +goose Up
-- +goose StatementBegin
-- Phase 10.14a: explicit active ship. Until now "the player's ship" was the
-- lowest-id ship they owned (sector.Pool.LookupPrimaryShipByPlayer). That rule
-- breaks fleet management (a bought ship has a higher id) and EVA (a spawned
-- spacesuit has a higher id). active_ship_id names the ship the player currently
-- controls. NULL = fall back to the min-id rule (backwards compatible for every
-- existing player). ON DELETE SET NULL so selling/destroying the active ship
-- cleanly reverts to the fallback instead of leaving a dangling pointer.
ALTER TABLE players
    ADD COLUMN active_ship_id BIGINT NULL REFERENCES ships(id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE players DROP COLUMN active_ship_id;
-- +goose StatementEnd
