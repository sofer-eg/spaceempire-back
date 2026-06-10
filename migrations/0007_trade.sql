-- +goose Up
-- +goose StatementBegin
ALTER TABLE players
    ADD COLUMN cash BIGINT NOT NULL DEFAULT 10000 CHECK (cash >= 0);

-- station_goods is the per-owner market: one row per (station, goods_type)
-- pair carries the buy/sell prices the station offers and the current stock.
-- owner_kind matches domain.EntityKind: 2=station, 4=trade_station, 5=pirbase.
-- A NULL sell_price means the station does not sell that good (buy-only);
-- a NULL buy_price means the station does not buy (sell-only). The CHECK
-- forbids the empty row (both NULL) so we never persist dead entries.
CREATE TABLE station_goods (
    owner_kind    SMALLINT NOT NULL,
    owner_id      BIGINT   NOT NULL,
    goods_type_id INTEGER  NOT NULL REFERENCES goods_types(id),
    buy_price     BIGINT,
    sell_price    BIGINT,
    stock         BIGINT   NOT NULL DEFAULT 0 CHECK (stock >= 0),
    max_stock     BIGINT   NOT NULL CHECK (max_stock > 0),
    PRIMARY KEY (owner_kind, owner_id, goods_type_id),
    CHECK (buy_price IS NOT NULL OR sell_price IS NOT NULL),
    CHECK (buy_price  IS NULL OR buy_price  > 0),
    CHECK (sell_price IS NULL OR sell_price > 0),
    CHECK (stock <= max_stock)
);

CREATE INDEX station_goods_owner_idx ON station_goods (owner_kind, owner_id);

-- Seed: minimum sample market so the front-end has something to render.
-- Prices are sized so a starter cash of 10000 buys a meaningful first
-- shipment without trivializing the loop. owner_kind constants kept inline
-- to keep this file self-contained (no Go enum at SQL load time).
--   2 = EntityKindStation
--   4 = EntityKindTradeStation
--   5 = EntityKindPirbase
--   goods_type_id: 1=Batteries, 2=Iron, 4=Crystals, 5=Computer Parts,
--                  7=Microchips, 40=Space Fuel
INSERT INTO station_goods (owner_kind, owner_id, goods_type_id, buy_price, sell_price, stock, max_stock) VALUES
    -- stations: each sells what it produces, buys raw inputs.
    (2, 1, 7,  NULL, 180, 200, 500),   -- station 1 (Argon, sector 1): sells Microchips
    (2, 1, 2,  40,   NULL, 50,  500),   -- station 1 buys Iron
    (2, 2, 5,  NULL, 140, 150, 500),   -- station 2 sells Computer Parts
    (2, 2, 1,  30,   NULL, 80,  500),   -- station 2 buys Batteries
    (2, 3, 4,  NULL, 110, 120, 400),   -- station 3 (Boron) sells Crystals
    (2, 3, 5,  90,   NULL, 30,  400),   -- station 3 buys Computer Parts
    (2, 4, 1,  NULL,  50,  250, 600),   -- station 4 sells Batteries
    (2, 4, 7,  150,  NULL,  0,  300),   -- station 4 buys Microchips
    (2, 5, 40, NULL,  70, 300, 800),   -- station 5 (Teladi) sells Space Fuel
    (2, 5, 4,  80,   NULL,  0,  300),   -- station 5 buys Crystals

    -- trade stations: broad buy+sell on most goods, slim margins.
    (4, 1, 1,  35,    55,  300, 1000),
    (4, 1, 2,  40,    65,  300, 1000),
    (4, 1, 7,  155,  185,  150, 600),
    (4, 2, 4,  85,   115,  200, 800),
    (4, 2, 5,  95,   135,  200, 800),
    (4, 2, 40, 55,    75,  400, 1200),
    (4, 3, 1,  35,    55,  300, 1000),
    (4, 3, 5,  95,   135,  100, 600),
    (4, 3, 40, 55,    75,  300, 1000),

    -- pirbase: smuggler hub that buys high-value goods and sells fuel.
    (5, 1, 7,  220,  NULL,   0,  300),  -- pirbase buys Microchips at premium
    (5, 1, 40, NULL,  60,  200,  500);  -- pirbase sells Space Fuel
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS station_goods;
ALTER TABLE players DROP COLUMN IF EXISTS cash;
-- +goose StatementEnd
