-- 0004_loader_version: exact mod loader build of a server (Fabric loader, Forge, NeoForge).
-- Empty for vanilla servers, including every server created before this migration.

ALTER TABLE servers ADD COLUMN loader_version TEXT NOT NULL DEFAULT '';
