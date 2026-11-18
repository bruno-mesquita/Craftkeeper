-- 0006_audit_server: the server an audit event is about. NULL for events that are not about
-- one server and for events recorded before this migration (their target holds the server
-- name). No foreign key: the history of a deleted server is kept.

ALTER TABLE audit_log ADD COLUMN server_id TEXT;
CREATE INDEX audit_log_server ON audit_log (server_id, id);
