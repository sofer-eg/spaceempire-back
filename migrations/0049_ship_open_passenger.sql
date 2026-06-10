-- +goose Up
-- +goose StatementBegin
-- Phase 10.23 (EVA): voluntary exit / hangar / boarding.
--   ships.is_open            — whether OTHER players may board this ship as a
--                              passenger. Players default closed; NPC ships are
--                              spawned open. Own ships are always boardable.
--   players.passenger_of_ship_id — the host ship a player currently rides as a
--                              passenger (NULL = not a passenger). Source of
--                              truth for the host's RAM PassengerPlayers mirror.
--                              ON DELETE SET NULL so a destroyed host cleanly
--                              drops the link (the kill sweep also ejects the
--                              passenger into a spacesuit).
ALTER TABLE ships
    ADD COLUMN is_open BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE players
    ADD COLUMN passenger_of_ship_id BIGINT NULL REFERENCES ships(id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE players DROP COLUMN passenger_of_ship_id;
ALTER TABLE ships   DROP COLUMN is_open;
-- +goose StatementEnd
