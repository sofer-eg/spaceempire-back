-- +goose Up
-- +goose StatementBegin
ALTER TABLE ships
    DROP COLUMN facing,
    ADD COLUMN direction_x DOUBLE PRECISION NOT NULL DEFAULT 1,
    ADD COLUMN direction_y DOUBLE PRECISION NOT NULL DEFAULT 0;
-- Reinterpret kinematic columns as per-tick (matches the SP physics
-- ported in phase 3.18 follow-up). Old per-second turn_rate=π/2 was
-- silently truncated to an instant turn at TickInterval=3s; π/4 per
-- tick (45°/tick ≈ 15°/sec) gives a visible "submarine" rotation.
ALTER TABLE ships
    ALTER COLUMN turn_rate SET DEFAULT 0.7853981633974483;
UPDATE ships
   SET turn_rate = 0.7853981633974483
 WHERE turn_rate >= 1.5707963267948966;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE ships
   SET turn_rate = 1.5707963267948966
 WHERE turn_rate = 0.7853981633974483;
ALTER TABLE ships
    ALTER COLUMN turn_rate SET DEFAULT 1.5707963267948966;
ALTER TABLE ships
    DROP COLUMN direction_y,
    DROP COLUMN direction_x,
    ADD COLUMN facing DOUBLE PRECISION NOT NULL DEFAULT 0;
-- +goose StatementEnd
