-- +goose Up
-- +goose StatementBegin
-- Phase 4.3: missiles cost one Missile cargo unit per launch (see
-- back/docs/specs/missiles.md §2). The id is chosen above the
-- 0006_cargo.sql seed range so future trade-good imports keep their
-- contiguous numbering.
INSERT INTO goods_types (id, name, space) VALUES (50, 'Missile', 2);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM goods_types WHERE id = 50;
-- +goose StatementEnd
