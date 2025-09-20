SELECT fields
FROM {{.tablename}}
WHERE key = ? AND deleted_at IS NULL
LIMIT 1;
