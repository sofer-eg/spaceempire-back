-- +goose Up
-- Ship class id (phase 10.13): the ct_ship_classes blueprint a ship was built
-- from. The combat ТТХ (max_speed/hull/shield/laser) are copied at spawn, but
-- the originating class id was lost — so the client could only guess the hull
-- shape from max_speed. Persisting it lets the WS snapshot carry a real
-- hullCategory (M1..TS) so the map renders the right silhouette per class.
-- Immutable after create (same nature as max_speed/max_hp). 0 = unknown /
-- spacesuit / legacy ship — the client then falls back to its maxSpeed
-- heuristic.
ALTER TABLE ships ADD COLUMN ship_class_id BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE ships DROP COLUMN ship_class_id;
