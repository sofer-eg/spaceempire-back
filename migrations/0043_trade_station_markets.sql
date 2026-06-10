-- +goose Up
-- +goose StatementBegin
-- Phase 10.19: give every sector-centre trade station a working market.
--
-- 0036 seeded station_goods only for the 5 racial-capital trade stations
-- (ids 1/5/9/13/17); the other 43 built trade stations (one per sector) had
-- no market and looked "dead" — you could dock but not trade. Seed each
-- remaining built trade station with the same accepted capital template: it
-- buys Batteries (1) and sells Iron (2) + Space Fuel (40). The buy/sell_price
-- columns are only a fallback — ranged goods are repriced dynamically from
-- stock (phase 10.18). Idempotent: skips trade stations that already trade.
INSERT INTO station_goods (owner_kind, owner_id, goods_type_id, buy_price, sell_price, stock, max_stock)
SELECT 4, ts.id, g.goods_type_id, g.buy_price, g.sell_price, g.stock, g.max_stock
FROM trade_stations ts
CROSS JOIN (VALUES
    (1,  35::bigint, NULL::bigint,   0::bigint, 600::bigint),
    (2,  NULL::bigint, 65::bigint, 200::bigint, 800::bigint),
    (40, NULL::bigint, 75::bigint, 300::bigint, 800::bigint)
) AS g(goods_type_id, buy_price, sell_price, stock, max_stock)
WHERE ts.built
  AND NOT EXISTS (
    SELECT 1 FROM station_goods sg
    WHERE sg.owner_kind = 4 AND sg.owner_id = ts.id
  );
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Drop the trade-station markets added here, keeping the 5 capitals (0036).
DELETE FROM station_goods
WHERE owner_kind = 4 AND owner_id NOT IN (1, 5, 9, 13, 17);
-- +goose StatementEnd
