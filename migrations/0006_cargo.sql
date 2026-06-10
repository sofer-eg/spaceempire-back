-- +goose Up
-- +goose StatementBegin
ALTER TABLE ships          ADD COLUMN cargobay DOUBLE PRECISION NOT NULL DEFAULT 100;
ALTER TABLE stations       ADD COLUMN cargobay DOUBLE PRECISION NOT NULL DEFAULT 10000;
ALTER TABLE trade_stations ADD COLUMN cargobay DOUBLE PRECISION NOT NULL DEFAULT 50000;

CREATE TABLE goods_types (
    id    INTEGER          PRIMARY KEY,
    name  TEXT             NOT NULL,
    space DOUBLE PRECISION NOT NULL CHECK (space >= 0)
);

-- Seed: core trade goods carried over from StarWind config.tp.php. Names
-- intentionally English-only here; UI translation is a frontend concern.
INSERT INTO goods_types (id, name, space) VALUES
    ( 1, 'Batteries',        1),
    ( 2, 'Iron',              2),
    ( 3, 'Silicon Wafers',    5),
    ( 4, 'Crystals',          3),
    ( 5, 'Computer Parts',    2),
    ( 6, 'Warheads',          5),
    ( 7, 'Microchips',        1),
    ( 8, 'Iron Ore',          2),
    ( 9, 'Silicon',           9),
    (40, 'Space Fuel',        1);

CREATE TABLE cargo (
    id            BIGSERIAL PRIMARY KEY,
    goods_type_id INTEGER   NOT NULL REFERENCES goods_types(id),
    quantity      BIGINT    NOT NULL CHECK (quantity >= 0),
    owner_kind    SMALLINT  NOT NULL,
    owner_id      BIGINT    NOT NULL,
    UNIQUE (owner_kind, owner_id, goods_type_id)
);

CREATE INDEX cargo_owner_idx ON cargo (owner_kind, owner_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS cargo;
DROP TABLE IF EXISTS goods_types;
ALTER TABLE trade_stations DROP COLUMN IF EXISTS cargobay;
ALTER TABLE stations       DROP COLUMN IF EXISTS cargobay;
ALTER TABLE ships          DROP COLUMN IF EXISTS cargobay;
-- +goose StatementEnd
