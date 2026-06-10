-- +goose Up
-- +goose StatementBegin
ALTER TABLE ships
    ADD COLUMN energy            INTEGER          NOT NULL DEFAULT 100,
    ADD COLUMN max_energy        INTEGER          NOT NULL DEFAULT 100,
    ADD COLUMN energy_recharge   INTEGER          NOT NULL DEFAULT 2,
    ADD COLUMN laser_damage      INTEGER          NOT NULL DEFAULT 10,
    ADD COLUMN laser_range       DOUBLE PRECISION NOT NULL DEFAULT 400,
    ADD COLUMN laser_energy_cost INTEGER          NOT NULL DEFAULT 5,
    ADD COLUMN attack_kind       SMALLINT,
    ADD COLUMN attack_id         BIGINT,
    ADD CONSTRAINT ships_attack_target_all_or_none CHECK (
        (attack_kind IS NULL AND attack_id IS NULL) OR
        (attack_kind IS NOT NULL AND attack_id IS NOT NULL)
    );
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ships
    DROP CONSTRAINT ships_attack_target_all_or_none,
    DROP COLUMN attack_id,
    DROP COLUMN attack_kind,
    DROP COLUMN laser_energy_cost,
    DROP COLUMN laser_range,
    DROP COLUMN laser_damage,
    DROP COLUMN energy_recharge,
    DROP COLUMN max_energy,
    DROP COLUMN energy;
-- +goose StatementEnd
