-- +goose Up
-- +goose StatementBegin
CREATE TABLE sectors (
    id    BIGINT           PRIMARY KEY,
    name  TEXT             NOT NULL,
    min_x DOUBLE PRECISION NOT NULL,
    min_y DOUBLE PRECISION NOT NULL,
    max_x DOUBLE PRECISION NOT NULL,
    max_y DOUBLE PRECISION NOT NULL
);

CREATE TABLE gates (
    id       BIGSERIAL        PRIMARY KEY,
    sector_a BIGINT           NOT NULL REFERENCES sectors(id),
    pos_a_x  DOUBLE PRECISION NOT NULL,
    pos_a_y  DOUBLE PRECISION NOT NULL,
    sector_b BIGINT           NOT NULL REFERENCES sectors(id),
    pos_b_x  DOUBLE PRECISION NOT NULL,
    pos_b_y  DOUBLE PRECISION NOT NULL,
    CHECK (sector_a <> sector_b)
);

CREATE INDEX gates_sector_a_idx ON gates(sector_a);
CREATE INDEX gates_sector_b_idx ON gates(sector_b);

-- Seed: 5x2 grid of 10 sectors with horizontal and vertical gates.
-- Each sector is a 2000x2000 box in its own local coordinate space
-- centred at the origin. Gates sit ±900 from the centre on the side
-- facing the adjacent sector.
INSERT INTO sectors (id, name, min_x, min_y, max_x, max_y) VALUES
    (1,  'Sector 1',  -1000, -1000, 1000, 1000),
    (2,  'Sector 2',  -1000, -1000, 1000, 1000),
    (3,  'Sector 3',  -1000, -1000, 1000, 1000),
    (4,  'Sector 4',  -1000, -1000, 1000, 1000),
    (5,  'Sector 5',  -1000, -1000, 1000, 1000),
    (6,  'Sector 6',  -1000, -1000, 1000, 1000),
    (7,  'Sector 7',  -1000, -1000, 1000, 1000),
    (8,  'Sector 8',  -1000, -1000, 1000, 1000),
    (9,  'Sector 9',  -1000, -1000, 1000, 1000),
    (10, 'Sector 10', -1000, -1000, 1000, 1000);

INSERT INTO gates (sector_a, pos_a_x, pos_a_y, sector_b, pos_b_x, pos_b_y) VALUES
    -- horizontal, top row
    (1,  900,    0, 2,  -900,    0),
    (2,  900,    0, 3,  -900,    0),
    (3,  900,    0, 4,  -900,    0),
    (4,  900,    0, 5,  -900,    0),
    -- horizontal, bottom row
    (6,  900,    0, 7,  -900,    0),
    (7,  900,    0, 8,  -900,    0),
    (8,  900,    0, 9,  -900,    0),
    (9,  900,    0, 10, -900,    0),
    -- vertical, top to bottom
    (1,    0,  900, 6,     0, -900),
    (2,    0,  900, 7,     0, -900),
    (3,    0,  900, 8,     0, -900),
    (4,    0,  900, 9,     0, -900),
    (5,    0,  900, 10,    0, -900);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS gates;
DROP TABLE IF EXISTS sectors;
-- +goose StatementEnd
