-- +goose Up
-- +goose StatementBegin
-- Phase 5.1: per-ship NPC AI controller state. A row marks a ship as
-- AI-controlled: controller_kind names the controller to rebuild at
-- cold-start, state_json carries that controller's serialized phase (route
-- progress, current target, …) so a restart resumes mid-decision. Loaded
-- per sector at worker startup, periodically snapshotted, flushed on
-- graceful shutdown. See docs/tasks/phase5-01-ai-runtime.md.
CREATE TABLE ai_state (
    ship_id         BIGINT      PRIMARY KEY REFERENCES ships(id) ON DELETE CASCADE,
    sector_id       BIGINT      NOT NULL,
    controller_kind TEXT        NOT NULL,
    state_json      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_ai_state_sector ON ai_state (sector_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE ai_state;
-- +goose StatementEnd
