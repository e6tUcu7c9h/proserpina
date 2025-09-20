CREATE TABLE IF NOT EXISTS {{.tablename}} (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL,
    fields TEXT NOT NULL CHECK (json_valid(fields)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    deleted_at TEXT DEFAULT NULL
);

-- Ensure only one current row (deleted_at IS NULL) per key for this table
CREATE UNIQUE INDEX IF NOT EXISTS "ux_{{.tablename_raw}}_key_current"
    ON {{.tablename}}(key)
    WHERE deleted_at IS NULL;

-- Helpful indexes for historical queries
CREATE INDEX IF NOT EXISTS "idx_{{.tablename_raw}}_deleted_at"
    ON {{.tablename}}(deleted_at);

CREATE INDEX IF NOT EXISTS "idx_{{.tablename_raw}}_key_deleted_at"
    ON {{.tablename}}(key, deleted_at);
