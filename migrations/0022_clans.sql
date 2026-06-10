-- +goose Up
-- +goose StatementBegin
-- Phase 6.1: player clans/alliances. A player belongs to at most one clan
-- (clan_members.player_id is the PK). Roles: 'leader' | 'officer' | 'member'
-- — phase 6.1 only creates leader (the founder) and member (accepted
-- invitee); promotion to officer is a later task. treasury_cash is reserved
-- for the optional shared treasury (no operations yet). See
-- docs/tasks/phase6-01-clans.md.
CREATE TABLE clans (
    id            BIGSERIAL   PRIMARY KEY,
    name          TEXT        NOT NULL UNIQUE,
    tag           TEXT        NOT NULL UNIQUE,
    leader_id     BIGINT      NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    treasury_cash BIGINT      NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE clan_members (
    player_id BIGINT      PRIMARY KEY REFERENCES players(id) ON DELETE CASCADE,
    clan_id   BIGINT      NOT NULL REFERENCES clans(id) ON DELETE CASCADE,
    role      TEXT        NOT NULL,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_clan_members_clan ON clan_members (clan_id);

CREATE TABLE clan_invitations (
    clan_id    BIGINT      NOT NULL REFERENCES clans(id) ON DELETE CASCADE,
    player_id  BIGINT      NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    invited_by BIGINT      NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    status     TEXT        NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (clan_id, player_id)
);
CREATE INDEX idx_clan_invitations_player ON clan_invitations (player_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE clan_invitations;
DROP TABLE clan_members;
DROP TABLE clans;
-- +goose StatementEnd
