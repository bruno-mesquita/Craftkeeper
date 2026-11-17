-- 0002_servers: game servers managed by the panel.
-- Status is not stored: it is derived from the container state on read.
-- game/loader/version are validated by the application (no CHECK), so new loaders do not
-- require rebuilding the table.

CREATE TABLE servers (
    id             TEXT    PRIMARY KEY,
    name           TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    description    TEXT    NOT NULL DEFAULT '',
    game           TEXT    NOT NULL,
    loader         TEXT    NOT NULL,
    version        TEXT    NOT NULL,
    memory_mb      INTEGER NOT NULL,
    cpu_millicores INTEGER NOT NULL,
    port           INTEGER NOT NULL UNIQUE,
    container_id   TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
