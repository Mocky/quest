-- schema v6 — remove type column from tasks; `bug` becomes a tag.
-- qst-1h retired the `type` field from the data model after concluding
-- the enum had only two values (`task`, `bug`), gated no lifecycle, and
-- its one structural use — the source-type constraint on `caused-by` and
-- `discovered-from` — was actively harmful (non-bug work can legitimately
-- cause or discover bugs). `bug` is reclassified as an ordinary tag so
-- planners can still mark bug tasks; retrospective queries now join on
-- relationships plus a tag filter rather than a type column.
--
-- Existing data preservation: every pre-migration row with `type='bug'`
-- gains a `bug` tag via INSERT OR IGNORE against tags(task_id, tag) so a
-- pre-existing `bug` tag (hand-set or carried through from earlier work)
-- is not duplicated.
--
-- The type column is dropped via the CREATE/INSERT/DROP/RENAME table-
-- recreation pattern already used by 003 and 004. migrate.go's
-- connection-level foreign_keys=OFF and commit-time foreign_key_check
-- keep the rename safe for FK-bearing side tables (dependencies,
-- history, tags, prs, notes, subtask_counter) that reference tasks(id).

-- Preserve bug-ness as a tag before the column goes away.
INSERT OR IGNORE INTO tags (task_id, tag)
    SELECT id, 'bug' FROM tasks WHERE type = 'bug';

-- Rebuild tasks without the type column. Every other column (including
-- the severity column and CHECK constraint from 004, and the status
-- CHECK from 003) is preserved exactly.
CREATE TABLE tasks_new (
    id                  TEXT PRIMARY KEY,
    title               TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    context             TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open','accepted','completed','failed','cancelled')),
    role                TEXT,
    tier                TEXT,
    severity            TEXT
        CHECK (severity IS NULL OR severity IN ('critical','high','medium','low')),
    acceptance_criteria TEXT,
    metadata            TEXT NOT NULL DEFAULT '{}',
    parent              TEXT,
    owner_session       TEXT,
    started_at          TEXT,
    completed_at        TEXT,
    handoff             TEXT,
    handoff_session     TEXT,
    handoff_written_at  TEXT,
    debrief             TEXT,
    created_at          TEXT NOT NULL,
    FOREIGN KEY(parent) REFERENCES tasks(id) ON UPDATE CASCADE
);

INSERT INTO tasks_new (
    id, title, description, context, status, role, tier, severity,
    acceptance_criteria, metadata, parent, owner_session,
    started_at, completed_at, handoff, handoff_session,
    handoff_written_at, debrief, created_at
) SELECT
    id, title, description, context, status, role, tier, severity,
    acceptance_criteria, metadata, parent, owner_session,
    started_at, completed_at, handoff, handoff_session,
    handoff_written_at, debrief, created_at
FROM tasks;

DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;

CREATE INDEX idx_tasks_parent ON tasks(parent);
CREATE INDEX idx_tasks_status ON tasks(status);
CREATE INDEX idx_tasks_status_role ON tasks(status, role);

UPDATE meta SET value = '6' WHERE key = 'schema_version';
