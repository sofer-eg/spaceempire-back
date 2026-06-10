-- +goose Up
-- +goose StatementBegin
CREATE TABLE stations (
    id        BIGSERIAL        PRIMARY KEY,
    owner_id  BIGINT           REFERENCES players(id) ON DELETE SET NULL,
    type      INTEGER          NOT NULL DEFAULT 0,
    sector_id BIGINT           NOT NULL REFERENCES sectors(id),
    pos_x     DOUBLE PRECISION NOT NULL,
    pos_y     DOUBLE PRECISION NOT NULL,
    hp        INTEGER          NOT NULL DEFAULT 860000,
    shield    INTEGER          NOT NULL DEFAULT 4300000,
    race      INTEGER          NOT NULL DEFAULT 0,
    built     BOOLEAN          NOT NULL DEFAULT TRUE
);

CREATE INDEX stations_sector_id_idx ON stations(sector_id);

CREATE TABLE shipyards (
    id        BIGSERIAL        PRIMARY KEY,
    owner_id  BIGINT           REFERENCES players(id) ON DELETE SET NULL,
    sector_id BIGINT           NOT NULL REFERENCES sectors(id),
    pos_x     DOUBLE PRECISION NOT NULL,
    pos_y     DOUBLE PRECISION NOT NULL,
    hp        INTEGER          NOT NULL DEFAULT 86300000,
    shield    INTEGER          NOT NULL DEFAULT 86300000,
    race      INTEGER          NOT NULL DEFAULT 0,
    built     BOOLEAN          NOT NULL DEFAULT TRUE
);

CREATE INDEX shipyards_sector_id_idx ON shipyards(sector_id);

CREATE TABLE trade_stations (
    id        BIGSERIAL        PRIMARY KEY,
    owner_id  BIGINT           REFERENCES players(id) ON DELETE SET NULL,
    type      INTEGER          NOT NULL DEFAULT 0,
    sector_id BIGINT           NOT NULL REFERENCES sectors(id),
    pos_x     DOUBLE PRECISION NOT NULL,
    pos_y     DOUBLE PRECISION NOT NULL,
    hp        INTEGER          NOT NULL DEFAULT 2590000,
    shield    INTEGER          NOT NULL DEFAULT 12950000,
    race      INTEGER          NOT NULL DEFAULT 0,
    built     BOOLEAN          NOT NULL DEFAULT TRUE
);

CREATE INDEX trade_stations_sector_id_idx ON trade_stations(sector_id);

CREATE TABLE pirbases (
    id        BIGSERIAL        PRIMARY KEY,
    sector_id BIGINT           NOT NULL REFERENCES sectors(id),
    pos_x     DOUBLE PRECISION NOT NULL,
    pos_y     DOUBLE PRECISION NOT NULL,
    hp        INTEGER          NOT NULL DEFAULT 100000,
    shield    INTEGER          NOT NULL DEFAULT 100000,
    angle     DOUBLE PRECISION NOT NULL DEFAULT 0,
    race      INTEGER          NOT NULL DEFAULT 6,
    built     BOOLEAN          NOT NULL DEFAULT TRUE
);

CREATE INDEX pirbases_sector_id_idx ON pirbases(sector_id);

-- Seed: 5 stations / 2 shipyards / 3 trade_stations / 1 pirbase across the
-- starter map (sectors 1..5). Coordinates kept within the SPA canvas
-- viewport (±200 / ±150) — sectors are 2000×2000 in the DB but the
-- current front-end UI shows a tighter 400×300 frame. Gates live at
-- ±900 so there's no conflict with seed positions here.
INSERT INTO stations (sector_id, pos_x, pos_y, type, race) VALUES
    (1,  100,  100, 0, 1),
    (2, -150,   80, 0, 1),
    (3,  150, -100, 0, 2),
    (4, -100, -120, 0, 2),
    (5,  180,   50, 0, 3);

INSERT INTO shipyards (sector_id, pos_x, pos_y, race) VALUES
    (1, -180,  -80, 1),
    (4,  180,  100, 2);

INSERT INTO trade_stations (sector_id, pos_x, pos_y, race) VALUES
    (1,  0,    0, 1),
    (2,  0,    0, 1),
    (3,  0,    0, 2);

INSERT INTO pirbases (sector_id, pos_x, pos_y, race) VALUES
    (5, -180, -120, 6);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS pirbases;
DROP TABLE IF EXISTS trade_stations;
DROP TABLE IF EXISTS shipyards;
DROP TABLE IF EXISTS stations;
-- +goose StatementEnd
