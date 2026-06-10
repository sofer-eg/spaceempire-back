-- +goose Up
-- Quest engine v2 (phase 8.17): per-quest deadline for the failed status.
-- status already TEXT (supports 'failed'/'abandoned'), step_index/state JSONB
-- already exist, and the PK (player_id, quest_id) already allows multiple
-- quests per player — only the deadline column is new.
ALTER TABLE player_quests ADD COLUMN deadline_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE player_quests DROP COLUMN deadline_at;
