-- 0001_init: users, sessions, settings, audit log.
-- All timestamps are INTEGER Unix seconds (UTC). IDs are opaque TEXT.

CREATE TABLE users (
    id            TEXT    PRIMARY KEY,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL CHECK (role IN ('admin')),
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

-- token_hash is the hex SHA-256 of the session cookie value; the raw token is never stored.
CREATE TABLE sessions (
    token_hash   TEXT    PRIMARY KEY,
    user_id      TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    csrf_token   TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    ip           TEXT    NOT NULL,
    user_agent   TEXT    NOT NULL
);
CREATE INDEX sessions_user_id ON sessions (user_id);
CREATE INDEX sessions_expires_at ON sessions (expires_at);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at INTEGER NOT NULL,
    user_id    TEXT,
    action     TEXT    NOT NULL,
    target     TEXT    NOT NULL DEFAULT '',
    ip         TEXT    NOT NULL DEFAULT '',
    success    INTEGER NOT NULL CHECK (success IN (0, 1))
);
CREATE INDEX audit_log_created_at ON audit_log (created_at);
