-- +goose Up
-- Ship radar range (phase 10.20 L1): the personal small-radar radius sourced
-- from the ship class (defaulted by category). Persisted like the other
-- derived stats (max_speed, …) — written at Create, read at LoadAll, folded by
-- up_scanner via the equipment pipeline (L3). 0 = legacy/spacesuit → the
-- subscription falls back to cfg.AOIRadius. See back/docs/specs/radar.md.
ALTER TABLE ships ADD COLUMN radar_range DOUBLE PRECISION NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE ships DROP COLUMN radar_range;
