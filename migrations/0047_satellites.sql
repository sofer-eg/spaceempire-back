-- +goose Up
-- +goose StatementBegin
-- Phase 10.15: player-deployed navigation satellites. A destructible static
-- sector object (EntityKind 11) modelled on laser_towers (0019): rendered by
-- the 10.13 silhouette, takes combat damage, and reveals the sector radar
-- while alive. No seed — satellites are created at runtime by the
-- install-satellite command (consumes goods id 26). Destruction is persisted
-- (row deleted) so a restart does not resurrect a killed satellite. See
-- back/docs/specs/satellite.md.
CREATE TABLE satellites (
    id              BIGSERIAL        PRIMARY KEY,
    owner_id        BIGINT           REFERENCES players(id) ON DELETE SET NULL,
    sector_id       BIGINT           NOT NULL REFERENCES sectors(id),
    pos_x           DOUBLE PRECISION NOT NULL,
    pos_y           DOUBLE PRECISION NOT NULL,
    race            INTEGER          NOT NULL DEFAULT 0,
    built           BOOLEAN          NOT NULL DEFAULT TRUE,
    hp              INTEGER          NOT NULL DEFAULT 5000,
    shield          INTEGER          NOT NULL DEFAULT 2000,
    max_shield      INTEGER          NOT NULL DEFAULT 2000,
    shield_recharge INTEGER          NOT NULL DEFAULT 20
);

CREATE INDEX satellites_sector_id_idx ON satellites(sector_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS satellites;
-- +goose StatementEnd
