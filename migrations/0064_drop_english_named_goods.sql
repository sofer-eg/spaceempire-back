-- +goose Up
-- +goose StatementBegin
-- TASK-177: take the two non-Russian goods out of the catalog.
--
-- 104 «Поцелуй Evening» and 114 'Gold ring with diamonds "charm"' were the only
-- two entries of the imported 80-good catalog whose name is not Russian — 114
-- entirely, 104 in one word. Both are faithful to the original StarWind data
-- (includes/types_prod.php carries those labels), so this is not converter
-- drift; it is the same defect as TASK-140 — a foreign-language label in a
-- Russian interface, shown in the hold, on every market, in auction lots and in
-- the trade scanner.
--
-- The customer chose deletion over translation knowing both were in live
-- circulation (49 market rows apiece, units in a player's hold): those units are
-- gone, and that is the accepted consequence.
--
-- The other half of the change is cmd/starwind-tools/convert-balance, which now
-- excludes both ids from configs/balance.yaml. That is what stops them coming
-- back: gen-trade-markets and gen-station-markets read the generated catalog, so
-- neither can seed a market row for a good that is no longer in it. Table and
-- file have to move together or
-- TestIntegration_BalanceCatalog_MatchesGoodsTypesTable (TASK-167) fails — it
-- compares the two in both directions.

-- An active lot holds the leading bid as escrow — the seller is paid only at
-- close (docs/specs/auction.md: «деньги не теряются»). Return it before the lot
-- goes, or the bidder's cash vanishes with it. Lots already closed (status 1) or
-- cancelled (2) have settled, so they are left alone.
UPDATE players p SET cash = p.cash + l.current_price
FROM auction_lots l
WHERE l.goods_type_id IN (104, 114)
  AND l.status = 0
  AND l.current_bidder_id = p.id;

-- goods_types last: cargo, station_goods and auction_lots each hold an FK to it
-- (and auction_bids cascades off auction_lots, so the bid history goes with the
-- lot on its own).
DELETE FROM auction_lots  WHERE goods_type_id IN (104, 114);
DELETE FROM cargo         WHERE goods_type_id IN (104, 114);
DELETE FROM station_goods WHERE goods_type_id IN (104, 114);
DELETE FROM goods_types   WHERE id IN (104, 114);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Partial by nature: the catalog rows come back, the goods do not.
--
-- What Down cannot restore is anything that referenced them — the hold units,
-- the auction lots and their bid history, the 98 market rows. Those were deleted
-- outright (no merge kept the pre-delete values, unlike 0063), so this Down
-- leaves every owner without them; the market rows would come back on a rerun of
-- gen-trade-markets / gen-station-markets, the player cargo would not.
--
-- Down alone also leaves the catalog inconsistent on purpose: configs/balance.yaml
-- is generated with both ids excluded, so as long as the converter's exclusion
-- list stands, TestIntegration_BalanceCatalog_MatchesGoodsTypesTable will fail
-- against these two rows. Reverting this migration means reverting the
-- exclusion in cmd/starwind-tools/convert-balance and regenerating the YAML too.
--
-- Names and space are those of the source catalog entries.
INSERT INTO goods_types (id, name, space) VALUES
    (104, 'Поцелуй Evening', 1),
    (114, 'Gold ring with diamonds "charm"', 1)
ON CONFLICT (id) DO NOTHING;
-- +goose StatementEnd
