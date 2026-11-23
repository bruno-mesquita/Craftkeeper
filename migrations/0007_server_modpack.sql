-- 0007_server_modpack: the catalog modpack (Modrinth or CurseForge) a server runs. The game
-- image installs and upgrades it from these values. All empty for servers without one,
-- including every server created before this migration.

ALTER TABLE servers ADD COLUMN modpack_source TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN modpack_project TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN modpack_slug TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN modpack_version TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN modpack_name TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN modpack_version_name TEXT NOT NULL DEFAULT '';
