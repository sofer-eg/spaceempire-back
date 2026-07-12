-- +goose Up
-- TASK-100.3.7: wall-clock timestamp of a ship's last seamless jump-drive hop
-- (port of SP DoJump's `updates.up_status` cooldown stamp). The jump-drive
-- command rejects a fresh jump while now-last_jump_at is below the real-time
-- cooldown (level 1 = 60 min, level 2 = 30 min). NULL = never jumped (no
-- cooldown) — the default for every existing ship. Written by the immediate
-- Save on a jump, read at LoadAll. See back/docs/specs/equipment_effects.md.
ALTER TABLE ships ADD COLUMN last_jump_at TIMESTAMPTZ NULL;

-- +goose Down
ALTER TABLE ships DROP COLUMN last_jump_at;
