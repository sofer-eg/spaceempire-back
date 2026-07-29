-- +goose Up
-- +goose StatementBegin
-- TASK-110: gates become destructible. They were the one static excluded from
-- the weapon-target set (ЧТЗ C-04) and so had no combat columns at all.
--
-- Durability sits deliberately between the deployables and the stations: a
-- jammer is 7500/4000 and a laser tower 50000-shielded, while a station carries
-- millions and never realistically dies. A gate is major infrastructure whose
-- loss cuts a sector link, so it must take a coordinated effort rather than one
-- frigate's afternoon — 250k hull behind a 100k shield that refills in ~200
-- ticks (shield/500, the same ratio 0028 used for the other statics).
--
-- destroyed is the wreck flag. A destroyed gate is not loaded into the topology
-- at all (see persistence/world loadGatesSQL), so the link stays severed across
-- restarts and nothing downstream needs to filter on it. Repair is TASK-67.
ALTER TABLE gates
    ADD COLUMN hp              INTEGER NOT NULL DEFAULT 250000,
    ADD COLUMN shield          INTEGER NOT NULL DEFAULT 100000,
    ADD COLUMN max_shield      INTEGER NOT NULL DEFAULT 100000,
    ADD COLUMN shield_recharge INTEGER NOT NULL DEFAULT 200,
    ADD COLUMN destroyed       BOOLEAN NOT NULL DEFAULT FALSE;

-- The loader reads only live gates, so make that lookup cheap.
CREATE INDEX gates_live_idx ON gates(id) WHERE NOT destroyed;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS gates_live_idx;
ALTER TABLE gates
    DROP COLUMN IF EXISTS hp,
    DROP COLUMN IF EXISTS shield,
    DROP COLUMN IF EXISTS max_shield,
    DROP COLUMN IF EXISTS shield_recharge,
    DROP COLUMN IF EXISTS destroyed;
-- +goose StatementEnd
