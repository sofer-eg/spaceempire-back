-- +goose Up
-- Phase 10.3.1: per-tick energy model. Net energy change contributed by the
-- ship's installed equipment — Σ energy_usage of "reverse" generators (+) minus
-- Σ energy_usage of "always" consumers (−). Persisted like the other derived
-- stats (max_energy, energy_recharge, radar_range, …): written at install/
-- uninstall (SaveEquipment), read at LoadAll, folded with energy_recharge by
-- combat.ChargeEnergy each tick. Default 0 keeps every existing / un-equipped
-- ship neutral. See back/docs/specs/equipment_effects.md.
ALTER TABLE ships ADD COLUMN energy_delta INT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE ships DROP COLUMN energy_delta;
