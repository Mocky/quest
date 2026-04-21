-- schema v3 — add CHECK constraints on enum columns.
-- The `complete` vs `completed` drift fixed by migration 002 confirmed
-- that Go-level guards alone cannot prevent invalid enum values: any
-- future code path that bypasses them, or any direct `sqlite3` write,
-- can plant a string the rest of the system does not understand.
-- Lifting enforcement to the schema itself makes the DB reject invalid
-- writes at the boundary.
--
-- Adding CHECK to an existing column requires the CREATE/INSERT/DROP/
-- RENAME table-recreation pattern. That pattern leaves FK references
-- dangling across the drop, and SQLite's commit-time FK check fires on
-- those dangling bindings even with PRAGMA defer_foreign_keys=ON. The
-- migration runner in migrate.go therefore takes a dedicated connection
-- and sets `PRAGMA foreign_keys=OFF` before opening the transaction;
-- after all migrations run it issues `PRAGMA foreign_key_check` inside
-- the tx so genuine integrity breaks still abort the commit and the
-- forward-only-never-partial contract is preserved.

-- tasks: CHECK on status and type.
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

-- dependencies: CHECK on link_type.
CREATE TABLE dependencies_new (
    task_id    TEXT NOT NULL,
    target_id  TEXT NOT NULL,
    link_type  TEXT NOT NULL
        CHECK (link_type IN ('blocked-by','caused-by','discovered-from','retry-of')),
    created_at TEXT NOT NULL,
    PRIMARY KEY (task_id, target_id, link_type),
    FOREIGN KEY(task_id)   REFERENCES tasks(id) ON UPDATE CASCADE,
    FOREIGN KEY(target_id) REFERENCES tasks(id) ON UPDATE CASCADE
);

INSERT INTO dependencies_new (task_id, target_id, link_type, created_at)
    SELECT task_id, target_id, link_type, created_at FROM dependencies;

DROP TABLE dependencies;
ALTER TABLE dependencies_new RENAME TO dependencies;

CREATE INDEX idx_dependencies_target ON dependencies(target_id);

-- history: CHECK on action.
CREATE TABLE history_new (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id   TEXT NOT NULL,
    timestamp TEXT NOT NULL,
    role      TEXT,
    session   TEXT,
    action    TEXT NOT NULL
        CHECK (action IN (
            'created','accepted','completed','failed','cancelled',
            'reset','moved','note_added','pr_added','field_updated',
            'linked','unlinked','tagged','untagged','handoff_set'
        )),
    payload   TEXT NOT NULL DEFAULT '{}',
    FOREIGN KEY(task_id) REFERENCES tasks(id) ON UPDATE CASCADE
);

INSERT INTO history_new (id, task_id, timestamp, role, session, action, payload)
    SELECT id, task_id, timestamp, role, session, action, payload FROM history;

DROP TABLE history;
ALTER TABLE history_new RENAME TO history;

CREATE INDEX idx_history_task_timestamp ON history(task_id, timestamp);
CREATE INDEX idx_history_timestamp ON history(timestamp);

UPDATE meta SET value = '3' WHERE key = 'schema_version';
