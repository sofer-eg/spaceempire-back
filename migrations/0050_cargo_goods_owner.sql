-- +goose Up
-- +goose StatementBegin
-- Phase 10.22: per-player ownership of goods deposited into a station hold.
--   cargo.goods_owner_id — the player who deposited this stack (the depositor),
--                          distinct from owner_kind/owner_id which encode WHERE
--                          the goods physically sit (ship / station / container).
--                          Sentinel 0 = unowned (NPC production, loot, container
--                          drops, ship holds) — takeable by anyone; a non-zero id
--                          is a personal deposit takeable only by that player.
--                          No FK: 0 has no matching players row by design.
-- The UNIQUE key gains goods_owner_id so two players can each hold their own
-- stack of the same goods type at the same station as separate rows.
ALTER TABLE cargo
    ADD COLUMN goods_owner_id BIGINT NOT NULL DEFAULT 0;

ALTER TABLE cargo
    DROP CONSTRAINT cargo_owner_kind_owner_id_goods_type_id_key;

ALTER TABLE cargo
    ADD CONSTRAINT cargo_owner_goods_uniq
    UNIQUE (owner_kind, owner_id, goods_type_id, goods_owner_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Collapse any per-depositor split back into one stack per (owner, goods_type)
-- before restoring the narrower UNIQUE, so the down migration cannot fail on a
-- duplicate-key violation. Sum the quantities into the surviving (lowest-id)
-- row, then drop the rest — no quantity is lost on rollback.
UPDATE cargo keep
SET quantity = agg.total
FROM (
    SELECT owner_kind, owner_id, goods_type_id,
           MIN(id) AS keep_id, SUM(quantity) AS total
    FROM cargo
    GROUP BY owner_kind, owner_id, goods_type_id
) agg
WHERE keep.id = agg.keep_id;

DELETE FROM cargo a
USING cargo b
WHERE a.owner_kind = b.owner_kind
  AND a.owner_id = b.owner_id
  AND a.goods_type_id = b.goods_type_id
  AND a.id > b.id;

ALTER TABLE cargo
    DROP CONSTRAINT cargo_owner_goods_uniq;

ALTER TABLE cargo
    ADD CONSTRAINT cargo_owner_kind_owner_id_goods_type_id_key
    UNIQUE (owner_kind, owner_id, goods_type_id);

ALTER TABLE cargo
    DROP COLUMN goods_owner_id;
-- +goose StatementEnd
