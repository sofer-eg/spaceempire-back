-- +goose Up
-- +goose StatementBegin
CREATE TABLE players (
    id            BIGSERIAL    PRIMARY KEY,
    login         TEXT         NOT NULL UNIQUE,
    password_hash TEXT         NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE sessions (
    token      TEXT         PRIMARY KEY,
    player_id  BIGINT       NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ  NOT NULL
);

CREATE INDEX sessions_player_id_idx ON sessions(player_id);
CREATE INDEX sessions_expires_at_idx ON sessions(expires_at);

CREATE TABLE ships (
    id         BIGSERIAL        PRIMARY KEY,
    player_id  BIGINT           NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    sector_id  BIGINT           NOT NULL,
    pos_x      DOUBLE PRECISION NOT NULL DEFAULT 0,
    pos_y      DOUBLE PRECISION NOT NULL DEFAULT 0,
    vel_x      DOUBLE PRECISION NOT NULL DEFAULT 0,
    vel_y      DOUBLE PRECISION NOT NULL DEFAULT 0,
    target_x   DOUBLE PRECISION,
    target_y   DOUBLE PRECISION,
    hp         INTEGER          NOT NULL DEFAULT 100,
    shield     INTEGER          NOT NULL DEFAULT 100,
    updated_at TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);

CREATE INDEX ships_player_id_idx ON ships(player_id);
CREATE INDEX ships_sector_id_idx ON ships(sector_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ships;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS players;
-- +goose StatementEnd
