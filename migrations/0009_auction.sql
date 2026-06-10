-- +goose Up
-- +goose StatementBegin
-- auction_lots: one row per lot a seller put up for bidding. status uses the
-- integer codes the Go layer's auction.Status mirrors (0=active, 1=closed,
-- 2=cancelled) so we don't need a Postgres enum. source_owner_kind/id
-- remember where the cargo came from so a no-bid cancel can refund it.
CREATE TABLE auction_lots (
    id                 BIGSERIAL PRIMARY KEY,
    seller_id          BIGINT      NOT NULL REFERENCES players(id),
    goods_type_id      INTEGER     NOT NULL REFERENCES goods_types(id),
    quantity           BIGINT      NOT NULL CHECK (quantity > 0),
    source_owner_kind  SMALLINT    NOT NULL,
    source_owner_id    BIGINT      NOT NULL,
    start_price        BIGINT      NOT NULL CHECK (start_price > 0),
    current_price      BIGINT      NOT NULL CHECK (current_price > 0),
    current_bidder_id  BIGINT      REFERENCES players(id),
    ends_at            TIMESTAMPTZ NOT NULL,
    status             SMALLINT    NOT NULL DEFAULT 0 CHECK (status IN (0, 1, 2)),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (current_price >= start_price),
    CHECK (current_bidder_id IS NULL OR current_bidder_id <> seller_id)
);

-- Partial index for the closer: scan only the small set of still-active lots
-- whose timer is up. The closer issues SELECT ... WHERE status=0 AND ends_at<=now.
CREATE INDEX auction_lots_active_ends_idx
    ON auction_lots (ends_at)
    WHERE status = 0;

CREATE INDEX auction_lots_seller_idx ON auction_lots (seller_id);
CREATE INDEX auction_lots_bidder_idx ON auction_lots (current_bidder_id);

-- auction_bids: append-only audit trail. CASCADE so dropping a lot in tests
-- removes its bid history too.
CREATE TABLE auction_bids (
    id         BIGSERIAL PRIMARY KEY,
    lot_id     BIGINT      NOT NULL REFERENCES auction_lots(id) ON DELETE CASCADE,
    bidder_id  BIGINT      NOT NULL REFERENCES players(id),
    amount     BIGINT      NOT NULL CHECK (amount > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX auction_bids_lot_idx ON auction_bids (lot_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS auction_bids;
DROP TABLE IF EXISTS auction_lots;
-- +goose StatementEnd
