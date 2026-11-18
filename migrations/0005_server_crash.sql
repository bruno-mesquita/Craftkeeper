-- 0005_server_crash: crash loop record of a server. The panel stops a server whose container
-- keeps being restarted by its restart policy and records when (Unix seconds, UTC) and after
-- how many automatic restarts. Cleared by the next start or restart through the panel.

ALTER TABLE servers ADD COLUMN crashed_at INTEGER;
ALTER TABLE servers ADD COLUMN crash_restarts INTEGER NOT NULL DEFAULT 0;
