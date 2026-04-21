-- schema v5 — add commits table and commit_added history action.
-- qst-14 adds a `commits` side table parallel to `prs`, recording git
-- BRANCH@HASH references that carried task output. Dedup/idempotency
-- rides a UNIQUE index on (task_id, branch, lower(hash)) — writes use
-- INSERT OR IGNORE so the app does not dedup in Go. The lower(hash)
-- expression future-proofs the dedup rule against a drifting validator
-- that might one day write uppercase; today's parser already lowercases
-- on write (spec §Commit reference format).
--
-- The history table's action CHECK (added in migration 003) must be
-- widened to include 'commit_added' before any such row can be written.
-- Adding a value to a CHECK constraint requires the SQLite
-- CREATE/INSERT/DROP/RENAME table-recreation pattern. migrate.go's
-- connection-level foreign_keys=OFF plus commit-time foreign_key_check
-- makes the recreation safe; the history table has no children but
-- tasks references it transitively via task_id FK, so FK handling is
-- identical to migration 003.

CREATE TABLE commits (
    task_id  TEXT NOT NULL,
    branch   TEXT NOT NULL,
    hash     TEXT NOT NULL,
    added_at TEXT NOT NULL,
    FOREIGN KEY(task_id) REFERENCES tasks(id) ON UPDATE CASCADE
);
CREATE UNIQUE INDEX idx_commits_task_branch_hash ON commits(task_id, branch, lower(hash));

-- history: widen action CHECK to include commit_added.
CREATE TABLE history_new (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id   TEXT NOT NULL,
    timestamp TEXT NOT NULL,
    role      TEXT,
    session   TEXT,
    action    TEXT NOT NULL
        CHECK (action IN (
            'created','accepted','completed','failed','cancelled',
            'reset','moved','note_added','pr_added','commit_added',
            'field_updated','linked','unlinked','tagged','untagged',
            'handoff_set'
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

UPDATE meta SET value = '5' WHERE key = 'schema_version';
