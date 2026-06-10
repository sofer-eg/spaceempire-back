-- +goose Up
-- Registration with race choice + named starter ship (phase 10.10).
-- players.race: the faction the player picked at registration (1..5 playable:
-- Argon/Boron/Paranid/Split/Teladi). 0 = unset (legacy rows / system __npc__).
-- Drives where the starter ship spawns (the race's home shipyard) and the
-- ship's race/name. Their actual ships now carry this race too (ships.race).
ALTER TABLE players ADD COLUMN race SMALLINT NOT NULL DEFAULT 0;

-- ships.name: the ship's display name (StarWind ships.name varchar(32)). The
-- starter ship is named after its M5 model for the spawning shipyard's race
-- (Argon -> "Разведчик", …). Empty for legacy/NPC rows — the client falls
-- back to the race name (NPC) or SHIP-<id>.
ALTER TABLE ships ADD COLUMN name VARCHAR(32) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE ships DROP COLUMN name;
ALTER TABLE players DROP COLUMN race;
