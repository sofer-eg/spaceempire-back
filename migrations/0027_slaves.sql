-- +goose Up
-- +goose StatementBegin
-- Phase 5.6: "Slaves" contraband. A killed passenger NPC spills a Slaves
-- container (goods 323); the only buyers are pirate bases. Rather than a
-- bespoke endpoint, slaves reuse the trade machinery: each pirbase gets a
-- buy-only market entry, so the existing dock -> market -> sell flow (and its
-- front-end) works unchanged. owner_kind 5 = EntityKindPirbase.
INSERT INTO goods_types (id, name, space) VALUES (323, 'Slaves', 1);

INSERT INTO station_goods (owner_kind, owner_id, goods_type_id, buy_price, sell_price, stock, max_stock)
SELECT 5, id, 323, 800, NULL, 0, 1000000000 FROM pirbases;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM station_goods WHERE goods_type_id = 323;
DELETE FROM goods_types WHERE id = 323;
-- +goose StatementEnd
