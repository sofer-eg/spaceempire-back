-- +goose Up
-- Ship equipment (phase 10.14): the list of installed ct_updates modules,
-- stored as a JSONB array of {equipmentID, type, level}. Stat modules fold
-- into the ships' max_speed/max_shield/energy/laser columns at install time;
-- capability modules live here until their subsystem consumes them. Empty
-- ('[]') for NPC/legacy/spacesuit ships.
ALTER TABLE ships ADD COLUMN equipment JSONB NOT NULL DEFAULT '[]'::jsonb;

-- +goose Down
ALTER TABLE ships DROP COLUMN equipment;
