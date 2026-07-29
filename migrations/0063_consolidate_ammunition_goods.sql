-- +goose Up
-- +goose StatementBegin
-- TASK-167: put the goods catalog back on ONE shape.
--
-- Migrations 0017/0018 invented two English-named duplicates for ammunition the
-- imported catalog already carried: 50 'Missile' and 51 'Combat Drone'. That
-- broke the loop, not just the labels — nothing ever put 50/51 on a market, so a
-- player who spent the starter magazine could not restock anywhere, while the
-- catalog rows that ARE sold on 59-72 station markets (10-14 missiles, 21 combat
-- drone) were consumed by nothing and were a pointless purchase.
--
-- The canonical class -> goods mapping comes from the legacy StarWind schema,
-- which keys every launchable on a goods id: ct_missiles.cargo_id (class 1
-- «Москит» -> 10) and ct_drones.cargo_id (class 1 «Боевой дрон» -> 21). Every
-- other deployable already sits on those ids — torpedos 23/24, satellite 26,
-- jammer 27 — and 0054_torpedos.sql refused to mint a new one for exactly this
-- reason. 50/51 were the only exceptions.
--
-- The consolidation restores the catalog's own space along with the id: a missile
-- goes 2 -> 1 (cheaper to carry), a drone 2 -> 290. The drone is deliberately
-- back to being a big-ship weapon, as it is in StarWind, where the same hulls
-- carry the same cargobay.

-- Merge, never rewrite: cargo has UNIQUE (owner_kind, owner_id, goods_type_id,
-- goods_owner_id), so an owner already holding good 10 (or 21) would break a
-- plain UPDATE ... SET goods_type_id. The ON CONFLICT column list matches that
-- constraint exactly — a mismatched list fails in the planner (42P10) on every
-- execution, not just on a colliding row (TASK-151/155).
--
-- 50 and 51 map to different targets, so one statement can never hit the same
-- conflict row twice ("cannot affect row a second time").
INSERT INTO cargo (owner_kind, owner_id, goods_type_id, goods_owner_id, quantity)
SELECT owner_kind,
       owner_id,
       CASE goods_type_id WHEN 50 THEN 10 WHEN 51 THEN 21 END,
       goods_owner_id,
       quantity
FROM cargo
WHERE goods_type_id IN (50, 51)
ON CONFLICT (owner_kind, owner_id, goods_type_id, goods_owner_id)
DO UPDATE SET quantity = cargo.quantity + EXCLUDED.quantity;

-- Only now can the goods_types rows go: cargo.goods_type_id has an FK to them.
DELETE FROM cargo WHERE goods_type_id IN (50, 51);
DELETE FROM goods_types WHERE id IN (50, 51);
-- +goose StatementEnd

-- +goose StatementBegin
-- Same drift, second form. 0006_cargo.sql seeded the core trade goods with
-- English placeholder names ("UI translation is a frontend concern"); 0042
-- imported the real StarWind catalog but with ON CONFLICT DO NOTHING, so it
-- could not overwrite them, and 0027 added 'Slaves' after it. Eleven rows have
-- carried a machine label ever since, while configs/balance.yaml — what
-- GET /api/goods serves — has had the catalog name all along.
--
-- Aligning the table on the catalog is what lets the guard test
-- (TestIntegration_BalanceCatalog_MatchesGoodsTypesTable) compare names at all,
-- and it is the same defect TASK-140 reported: an English machine label in a
-- Russian interface. space already agrees for every one of them.
UPDATE goods_types AS g SET name = v.name
FROM (VALUES
    ( 1, 'Батарейки'),
    ( 2, 'Железо'),
    ( 3, 'Кремниевые пластины'),
    ( 4, 'Кристаллы'),
    ( 5, 'Компьютерные компоненты'),
    ( 6, 'Боеголовки'),
    ( 7, 'Микросхемы'),
    ( 8, 'Железная руда'),
    ( 9, 'Кремний'),
    (40, 'Космическое топливо'),
    (323, 'Рабы')
) AS v(id, name)
WHERE g.id = v.id;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Partial by nature, and deliberately so.
--
-- The name alignment reverses exactly. Re-creating the 50/51 goods_types rows
-- reverses exactly too. What CANNOT be reversed is the cargo merge: units that
-- were good 50 are now indistinguishable from units of good 10 the owner already
-- had (they were summed into one row), so this Down leaves them where they are —
-- as ordinary Ракета Москит / Боевой дрон. Splitting them back would require the
-- pre-merge quantities, which the merge did not keep.
--
-- Consequence: after Down the two goods exist again and the launch handlers of the
-- older build spend them, but every hold is empty of them. The starter magazine
-- (spawner.StartMissiles) refills on the next spawn.
INSERT INTO goods_types (id, name, space) VALUES
    (50, 'Missile', 2),
    (51, 'Combat Drone', 2)
ON CONFLICT (id) DO NOTHING;

UPDATE goods_types AS g SET name = v.name
FROM (VALUES
    ( 1, 'Batteries'),
    ( 2, 'Iron'),
    ( 3, 'Silicon Wafers'),
    ( 4, 'Crystals'),
    ( 5, 'Computer Parts'),
    ( 6, 'Warheads'),
    ( 7, 'Microchips'),
    ( 8, 'Iron Ore'),
    ( 9, 'Silicon'),
    (40, 'Space Fuel'),
    (323, 'Slaves')
) AS v(id, name)
WHERE g.id = v.id;
-- +goose StatementEnd
