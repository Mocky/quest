-- schema v2 — rename terminal status value `complete` → `completed`
-- per spec commit c5eae63. `status` has no CHECK constraint in schema
-- v1, so this is a data-only migration; no structural changes.

UPDATE tasks SET status = 'completed' WHERE status = 'complete';

UPDATE meta SET value = '2' WHERE key = 'schema_version';
