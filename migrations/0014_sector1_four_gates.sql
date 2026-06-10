-- +goose Up
-- +goose StatementBegin
-- Give Sector 1 a gate on each of its four sides so the Max-zoom map shows
-- a symmetric frame. 0002 seeded only the east (→ Sector 2) and south
-- (→ Sector 6) gates; add the west (→ Sector 5) and north (→ Sector 10)
-- edges. Positions follow the same ±900 convention. With all four sides
-- present the statics+gates bounding box is centred on the origin, so the
-- left/right gates render at the vertical middle of the sector map.
INSERT INTO gates (sector_a, pos_a_x, pos_a_y, sector_b, pos_b_x, pos_b_y) VALUES
    (1, -900,    0, 5,  900,    0),  -- west  ↔ Sector 5 east
    (1,    0, -900, 10,    0,  900); -- north ↔ Sector 10 south
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM gates
 WHERE (sector_a = 1 AND sector_b = 5  AND pos_a_x = -900 AND pos_a_y =    0)
    OR (sector_a = 1 AND sector_b = 10 AND pos_a_x =    0 AND pos_a_y = -900);
-- +goose StatementEnd
