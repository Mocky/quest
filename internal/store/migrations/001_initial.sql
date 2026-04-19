-- schema v1 — initial quest schema.
-- Task 3.2: bootstraps meta, tasks, history, dependencies, tags,
-- prs, notes, and the two ID counters. Every side table declares
-- FOREIGN KEY(task_id) ... ON UPDATE CASCADE so quest move can
-- rewrite every referencing row atomically with a single
-- UPDATE tasks SET id = ?.

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE tasks (
    id                  TEXT PRIMARY KEY,
    title               TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    context             TEXT NOT NULL DEFAULT '',
    type                TEXT NOT NULL DEFAULT 'task',
    status              TEXT NOT NULL DEFAULT 'open',
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
CREATE INDEX idx_tasks_parent ON tasks(parent);
CREATE INDEX idx_tasks_status ON tasks(status);
CREATE INDEX idx_tasks_status_role ON tasks(status, role);

CREATE TABLE history (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id   TEXT NOT NULL,
    timestamp TEXT NOT NULL,
    role      TEXT,
    session   TEXT,
    action    TEXT NOT NULL,
    payload   TEXT NOT NULL DEFAULT '{}',
    FOREIGN KEY(task_id) REFERENCES tasks(id) ON UPDATE CASCADE
);
CREATE INDEX idx_history_task_timestamp ON history(task_id, timestamp);
CREATE INDEX idx_history_timestamp ON history(timestamp);

CREATE TABLE dependencies (
    task_id    TEXT NOT NULL,
    target_id  TEXT NOT NULL,
    link_type  TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (task_id, target_id, link_type),
    FOREIGN KEY(task_id)   REFERENCES tasks(id) ON UPDATE CASCADE,
    FOREIGN KEY(target_id) REFERENCES tasks(id) ON UPDATE CASCADE
);
CREATE INDEX idx_dependencies_target ON dependencies(target_id);

CREATE TABLE tags (
    task_id TEXT NOT NULL,
    tag     TEXT NOT NULL,
    PRIMARY KEY (task_id, tag),
    FOREIGN KEY(task_id) REFERENCES tasks(id) ON UPDATE CASCADE
);
CREATE INDEX idx_tags_tag ON tags(tag);

CREATE TABLE prs (
    task_id  TEXT NOT NULL,
    url      TEXT NOT NULL,
    added_at TEXT NOT NULL,
    PRIMARY KEY (task_id, url),
    FOREIGN KEY(task_id) REFERENCES tasks(id) ON UPDATE CASCADE
);

CREATE TABLE notes (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id   TEXT NOT NULL,
    timestamp TEXT NOT NULL,
    body      TEXT NOT NULL,
    FOREIGN KEY(task_id) REFERENCES tasks(id) ON UPDATE CASCADE
);
CREATE INDEX idx_notes_task_timestamp ON notes(task_id, timestamp);

CREATE TABLE task_counter (
    prefix     TEXT PRIMARY KEY,
    next_value INTEGER NOT NULL
);

CREATE TABLE subtask_counter (
    parent_id  TEXT PRIMARY KEY,
    next_value INTEGER NOT NULL,
    FOREIGN KEY(parent_id) REFERENCES tasks(id) ON UPDATE CASCADE ON DELETE NO ACTION
);

INSERT INTO meta(key, value) VALUES ('schema_version', '1');
