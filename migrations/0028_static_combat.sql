-- +goose Up
-- +goose StatementBegin
-- Phase 6.2b: static objects become destructible. They already have hp/shield;
-- add the shield cap and per-tick recharge so TO_ObjectShieldCharge can be
-- ported (the loaded shield is the spawn value = full, max_shield records the
-- cap so a damaged shield can recharge back across restarts). Recharge defaults
-- are ~shield/200 (≈10 min to refill at a 3s tick) — tunable later via balance.
ALTER TABLE stations       ADD COLUMN max_shield INTEGER NOT NULL DEFAULT 4300000,  ADD COLUMN shield_recharge INTEGER NOT NULL DEFAULT 21500;
ALTER TABLE shipyards      ADD COLUMN max_shield INTEGER NOT NULL DEFAULT 86300000, ADD COLUMN shield_recharge INTEGER NOT NULL DEFAULT 431500;
ALTER TABLE trade_stations ADD COLUMN max_shield INTEGER NOT NULL DEFAULT 12950000, ADD COLUMN shield_recharge INTEGER NOT NULL DEFAULT 64750;
ALTER TABLE pirbases       ADD COLUMN max_shield INTEGER NOT NULL DEFAULT 100000,   ADD COLUMN shield_recharge INTEGER NOT NULL DEFAULT 500;
ALTER TABLE laser_towers   ADD COLUMN max_shield INTEGER NOT NULL DEFAULT 50000,    ADD COLUMN shield_recharge INTEGER NOT NULL DEFAULT 250;

-- Existing rows carry a current shield equal to their spawn value; seed
-- max_shield from it so they start at full and recharge to the right cap.
UPDATE stations       SET max_shield = shield;
UPDATE shipyards      SET max_shield = shield;
UPDATE trade_stations SET max_shield = shield;
UPDATE pirbases       SET max_shield = shield;
UPDATE laser_towers   SET max_shield = shield;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE stations       DROP COLUMN IF EXISTS max_shield, DROP COLUMN IF EXISTS shield_recharge;
ALTER TABLE shipyards      DROP COLUMN IF EXISTS max_shield, DROP COLUMN IF EXISTS shield_recharge;
ALTER TABLE trade_stations DROP COLUMN IF EXISTS max_shield, DROP COLUMN IF EXISTS shield_recharge;
ALTER TABLE pirbases       DROP COLUMN IF EXISTS max_shield, DROP COLUMN IF EXISTS shield_recharge;
ALTER TABLE laser_towers   DROP COLUMN IF EXISTS max_shield, DROP COLUMN IF EXISTS shield_recharge;
-- +goose StatementEnd
