-- +goose Up
-- +goose StatementBegin
-- Phase 8.12: per-player quest progress (tutorial + missions). Quest
-- definitions live in code (internal/quest); only progress is persisted. One
-- row per (player, quest); step_index is the current step, status active →
-- completed. state is reserved for per-step counters (unused by the MVP's
-- boolean-condition steps). See docs/specs/quest.md.
CREATE TABLE player_quests (
    player_id    BIGINT      NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    quest_id     TEXT        NOT NULL,
    step_index   SMALLINT    NOT NULL DEFAULT 0,
    status       TEXT        NOT NULL DEFAULT 'active',
    state        JSONB,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    PRIMARY KEY (player_id, quest_id)
);

-- Poller scans active quests.
CREATE INDEX idx_player_quests_active ON player_quests (player_id) WHERE status = 'active';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE player_quests;
-- +goose StatementEnd
