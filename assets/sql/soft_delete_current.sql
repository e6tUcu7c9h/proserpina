UPDATE {{.tablename}}
SET deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
WHERE key = ? AND deleted_at IS NULL;
