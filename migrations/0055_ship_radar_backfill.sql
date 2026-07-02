-- +goose Up
-- +goose StatementBegin
-- Backfill ships.radar_range for existing class ships (TASK-117), generated
-- by cmd/starwind-tools/gen-ship-radar. Migration 0046 added the column with
-- DEFAULT 0 and no backfill, so pre-0046 ships (incl. the player's own) sat at
-- radar_range=0 and fell back to the whole-sector radius. Each UPDATE assigns
-- the ship class's radar (balance category default) to the ships of that class
-- that still have no radar. Spacesuits and already-set ships are left alone.
-- Do not edit by hand — rerun the generator.

UPDATE ships SET radar_range = 2200
WHERE radar_range <= 0 AND is_spacesuit = false AND ship_class_id IN (81, 90, 99, 108, 117, 126, 135, 144);
UPDATE ships SET radar_range = 2400
WHERE radar_range <= 0 AND is_spacesuit = false AND ship_class_id IN (73, 74, 82, 83, 91, 92, 100, 101, 109, 110, 118, 119, 127, 128, 136, 137, 159);
UPDATE ships SET radar_range = 2600
WHERE radar_range <= 0 AND is_spacesuit = false AND ship_class_id IN (79, 88, 97, 106, 115, 124, 133, 142, 161, 169, 170);
UPDATE ships SET radar_range = 2800
WHERE radar_range <= 0 AND is_spacesuit = false AND ship_class_id IN (75, 76, 80, 84, 85, 89, 93, 94, 98, 102, 103, 107, 111, 112, 116, 120, 121, 125, 129, 130, 134, 138, 139, 143, 162, 163, 164, 165, 166, 167, 168);
UPDATE ships SET radar_range = 3000
WHERE radar_range <= 0 AND is_spacesuit = false AND ship_class_id IN (78, 87, 96, 105, 114, 123, 132, 141, 157, 158, 160);
UPDATE ships SET radar_range = 3500
WHERE radar_range <= 0 AND is_spacesuit = false AND ship_class_id IN (77, 86, 95, 104, 113, 122, 131, 140);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Undo the backfill: reset class ships back to radar_range=0 (spacesuits
-- untouched). The runtime fallback keeps them playable; a re-run of Up
-- restores the class radars.
UPDATE ships SET radar_range = 0
WHERE is_spacesuit = false AND ship_class_id IN (73, 74, 75, 76, 77, 78, 79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90, 91, 92, 93, 94, 95, 96, 97, 98, 99, 100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114, 115, 116, 117, 118, 119, 120, 121, 122, 123, 124, 125, 126, 127, 128, 129, 130, 131, 132, 133, 134, 135, 136, 137, 138, 139, 140, 141, 142, 143, 144, 157, 158, 159, 160, 161, 162, 163, 164, 165, 166, 167, 168, 169, 170);
-- +goose StatementEnd
