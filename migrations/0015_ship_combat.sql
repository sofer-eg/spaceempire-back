-- +goose Up
-- +goose StatementBegin
ALTER TABLE ships
    ADD COLUMN max_hp          INTEGER NOT NULL DEFAULT 100,
    ADD COLUMN max_shield      INTEGER NOT NULL DEFAULT 100,
    ADD COLUMN shield_recharge INTEGER NOT NULL DEFAULT 1;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ships
    DROP COLUMN shield_recharge,
    DROP COLUMN max_shield,
    DROP COLUMN max_hp;
-- +goose StatementEnd
