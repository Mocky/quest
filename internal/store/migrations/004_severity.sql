-- schema v4 — add nullable severity column on tasks with CHECK enum.
-- qst-1d adds an optional triage severity (critical|high|medium|low) to
-- every task, parallel to tier and role. The column is nullable so
-- existing rows survive the migration without a backfill; the CHECK
-- constraint enforces the enum at the storage boundary, matching the
-- precedent set by 003 for status, type, link_type, and action.
--
-- Adding a CHECK-bearing column to an existing table requires the same
-- CREATE/INSERT/DROP/RENAME table-recreation pattern 003 used — SQLite
-- does not support ALTER TABLE ADD CONSTRAINT. migrate.go already
-- disables foreign_keys=OFF on a dedicated connection and runs
-- PRAGMA foreign_key_check before commit, so the pattern replays
-- cleanly with the existing tx machinery.

CREATE TABLE tasks_new (
    id                  TEXT PRIMARY KEY,
    title               TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    context             TEXT NOT NULL DEFAULT '',
    type                TEXT NOT NULL DEFAULT 'task'
        CHECK (type IN ('task','bug')),
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
    id, title, description, context, type, status, role, tier,
    acceptance_criteria, metadata, parent, owner_session,
    started_at, completed_at, handoff, handoff_session,
    handoff_written_at, debrief, created_at
) SELECT
    id, title, description, context, type, status, role, tier,
    acceptance_criteria, metadata, parent, owner_session,
    started_at, completed_at, handoff, handoff_session,
    handoff_written_at, debrief, created_at
FROM tasks;

DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;

CREATE INDEX idx_tasks_parent ON tasks(parent);
CREATE INDEX idx_tasks_status ON tasks(status);
CREATE INDEX idx_tasks_status_role ON tasks(status, role);

UPDATE meta SET value = '4' WHERE key = 'schema_version';
