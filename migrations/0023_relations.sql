-- +goose Up
-- +goose StatementBegin
-- Phase 6.2: declared relations between entities (player↔player, clan↔clan,
-- and later player↔race). Keyed by a typed EntityRef pair (kind+id). Only
-- non-neutral declarations are stored — absence means RelationNeutral. The
-- fast-lookup "hostility cache" is kept in RAM by the relations Service
-- (Precount), not in a table: a ≤1µs lookup never hits the DB. See
-- back/docs/specs/relations.md.
CREATE TABLE relations (
    from_kind   SMALLINT    NOT NULL,
    from_id     BIGINT      NOT NULL,
    to_kind     SMALLINT    NOT NULL,
    to_id       BIGINT      NOT NULL,
    status      TEXT        NOT NULL,
    declared_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (from_kind, from_id, to_kind, to_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE relations;
-- +goose StatementEnd
