-- Schema-Version 1. Wird angewandt, wenn PRAGMA user_version = 0 ist.

CREATE TABLE nodes (
    id          INTEGER PRIMARY KEY,
    parent_id   INTEGER NULL REFERENCES nodes(id) ON DELETE CASCADE, -- NULL = oberste Ebene
    kind        TEXT    NOT NULL CHECK (kind IN ('folder', 'tx')),
    tcode       TEXT    NULL,                                        -- nur bei kind = 'tx'
    title       TEXT    NOT NULL,                                    -- Ordnername bzw. Kurztext
    description TEXT    NULL,                                        -- Freitext, mehrzeilig
    sort_index  INTEGER NOT NULL,                                    -- Reihenfolge je parent_id
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    CHECK ((kind = 'tx') = (tcode IS NOT NULL AND tcode <> ''))
);

CREATE INDEX nodes_parent_sort ON nodes (parent_id, sort_index);
