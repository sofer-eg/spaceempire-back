-- +goose Up
-- Spacesuit / EVA (phase 10.1): a ship flagged is_spacesuit is the weak pilot
-- "ship" a player drops into when their real ship is destroyed (ct_objects
-- 'Скафандр' in the original StarWind: hull 1, laser 5, no shield/cargo).
-- Destroying the spacesuit respawns the player a fresh ship at the home
-- shipyard. Immutable after create. Players/NPC ships are false.
ALTER TABLE ships ADD COLUMN is_spacesuit BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE ships DROP COLUMN is_spacesuit;
