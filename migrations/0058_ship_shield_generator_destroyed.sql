-- +goose Up
-- TASK-100.3.9.1: durable marker for a ship whose up_shield module was knocked
-- off in combat (SP DestroyModule). Once set, every fit recompute still forces
-- max_shield/shield to 0, so a destroyed shield generator stays destroyed across
-- later knockoffs and across cold-start — which is what opens the ship to
-- capture (TASK-100.3.9.4). Written via SaveEquipment (the knockoff / re-outfit
-- path), read at LoadAll. Default FALSE keeps every existing ship intact. See
-- back/docs/specs/destroy_module.md.
ALTER TABLE ships ADD COLUMN shield_generator_destroyed BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE ships DROP COLUMN shield_generator_destroyed;
