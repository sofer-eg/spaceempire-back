-- +goose Up
-- +goose StatementBegin
-- TASK-131: player-deployed hyper-interference generators ("Генератор
-- гипер-помех", SP ct_drones class 7). A destructible static sector object
-- (EntityKind 13) modelled on satellites (0047): rendered by the 10.13
-- silhouette, takes combat damage, and jams the seamless jump drive
-- (up_jump_drive) of every ship within Config.JammerRange while alive. No seed
-- — jammers are created at runtime by the install-jammer command (consumes
-- goods id 27). Destruction is persisted (row deleted) so a restart does not
-- resurrect a killed generator. See back/docs/specs/jammer.md.
CREATE TABLE jammers (
    id              BIGSERIAL        PRIMARY KEY,
    owner_id        BIGINT           REFERENCES players(id) ON DELETE SET NULL,
    sector_id       BIGINT           NOT NULL REFERENCES sectors(id),
    pos_x           DOUBLE PRECISION NOT NULL,
    pos_y           DOUBLE PRECISION NOT NULL,
    race            INTEGER          NOT NULL DEFAULT 0,
    built           BOOLEAN          NOT NULL DEFAULT TRUE,
    hp              INTEGER          NOT NULL DEFAULT 7500,
    shield          INTEGER          NOT NULL DEFAULT 4000,
    max_shield      INTEGER          NOT NULL DEFAULT 4000,
    shield_recharge INTEGER          NOT NULL DEFAULT 20
);

CREATE INDEX jammers_sector_id_idx ON jammers(sector_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS jammers;
-- +goose StatementEnd
