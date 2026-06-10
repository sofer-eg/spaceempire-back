-- +goose Up
-- +goose StatementBegin
ALTER TABLE ships
    ADD COLUMN docked_kind                SMALLINT,
    ADD COLUMN docked_id                  BIGINT,
    ADD COLUMN auto_pilot_module          BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN final_target_dock_kind     SMALLINT,
    ADD COLUMN final_target_dock_id       BIGINT;

ALTER TABLE ships
    ADD CONSTRAINT ships_docked_all_or_none CHECK (
        (docked_kind IS NULL AND docked_id IS NULL)
        OR
        (docked_kind IS NOT NULL AND docked_id IS NOT NULL)
    );

ALTER TABLE ships
    ADD CONSTRAINT ships_final_target_dock_all_or_none CHECK (
        (final_target_dock_kind IS NULL AND final_target_dock_id IS NULL)
        OR
        (final_target_dock_kind IS NOT NULL AND final_target_dock_id IS NOT NULL)
    );

-- Dock can only be set when the Course itself is set.
ALTER TABLE ships
    ADD CONSTRAINT ships_final_target_dock_requires_course CHECK (
        final_target_dock_kind IS NULL OR final_target_sector IS NOT NULL
    );
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ships
    DROP CONSTRAINT IF EXISTS ships_final_target_dock_requires_course,
    DROP CONSTRAINT IF EXISTS ships_final_target_dock_all_or_none,
    DROP CONSTRAINT IF EXISTS ships_docked_all_or_none,
    DROP COLUMN  IF EXISTS final_target_dock_id,
    DROP COLUMN  IF EXISTS final_target_dock_kind,
    DROP COLUMN  IF EXISTS auto_pilot_module,
    DROP COLUMN  IF EXISTS docked_id,
    DROP COLUMN  IF EXISTS docked_kind;
-- +goose StatementEnd
