-- +goose Up
-- +goose StatementBegin
-- TASK-89: persist layer for procedural quests.
--
-- player_quest_offers holds generated quest instances awaiting the player's
-- accept, each with a TTL (expires_at). template_id is the template kind for a
-- procedural offer OR the id of a static Def for a story offer. definition is
-- the frozen instance JSONB for procedural offers and NULL for story offers
-- (which resolve through the static Lookup(template_id)). source distinguishes
-- the pacer trigger that produced the offer ('dock' | 'space').
CREATE TABLE player_quest_offers (
    id          BIGSERIAL PRIMARY KEY,
    player_id   BIGINT      NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    template_id TEXT        NOT NULL,   -- kind шаблона (proc) ИЛИ id статического Def (сюжетный)
    definition  JSONB,                  -- NULL для сюжетных offer'ов (резолв через статический Lookup(template_id))
    source      TEXT        NOT NULL,   -- 'dock' | 'space'
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL
);
-- +goose StatementEnd
CREATE INDEX idx_player_quest_offers_player  ON player_quest_offers (player_id);
CREATE INDEX idx_player_quest_offers_expires ON player_quest_offers (expires_at);
-- +goose StatementBegin
-- player_quest_counters is the per-player pacer state: how many docks/jumps
-- have accrued toward the next offer and the randomised thresholds. One row per
-- player; UPSERT on each trigger. next_docks/next_jumps = 0 means the threshold
-- has not been rolled yet (the pacer rolls it on first use).
CREATE TABLE player_quest_counters (
    player_id  BIGINT NOT NULL PRIMARY KEY REFERENCES players(id) ON DELETE CASCADE,
    docks      INT NOT NULL DEFAULT 0,
    jumps      INT NOT NULL DEFAULT 0,
    next_docks INT NOT NULL DEFAULT 0,   -- 0 = порог ещё не разыгран (pacer перебросит)
    next_jumps INT NOT NULL DEFAULT 0
);
-- +goose StatementEnd
-- Frozen definition of an accepted procedural instance (NULL for static quests).
ALTER TABLE player_quests ADD COLUMN definition JSONB;

-- +goose Down
ALTER TABLE player_quests DROP COLUMN definition;
DROP TABLE player_quest_counters;
DROP TABLE player_quest_offers;
