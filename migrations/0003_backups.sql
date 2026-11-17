-- 0003_backups: backup archives and automatic backup schedules, per server.
-- Archives live in <data dir>/backups/<server id>/<backup id>.zip; rows go with their server.

CREATE TABLE backups (
    id           TEXT    PRIMARY KEY,
    server_id    TEXT    NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    kind         TEXT    NOT NULL CHECK (kind IN ('manual', 'scheduled')),
    status       TEXT    NOT NULL CHECK (status IN ('creating', 'ready', 'failed')),
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    failure      TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    completed_at INTEGER -- NULL while creating
);
CREATE INDEX backups_server_created ON backups (server_id, created_at);

-- next_run_at is 0 while the schedule is off (interval_hours = 0).
CREATE TABLE backup_schedules (
    server_id      TEXT    PRIMARY KEY REFERENCES servers (id) ON DELETE CASCADE,
    interval_hours INTEGER NOT NULL,
    retain         INTEGER NOT NULL,
    next_run_at    INTEGER NOT NULL
);
