# REST API — v1

Base path: `/api/v1`. JSON in and out (`Content-Type: application/json`). This document is a
contract: a different backend implementation (e.g. Rust) must behave identically.
Status: **implemented and covered by contract tests** (`internal/presentation/http/api_test.go`)
unless marked *planned*.

## Conventions

- Timestamps: RFC 3339, UTC (`2026-11-17T12:00:00Z`). IDs: opaque strings.
- Request bodies: a single JSON object, max 64 KiB, unknown fields rejected.
- Authentication: `mesquita_session` cookie (`HttpOnly`, `SameSite=Strict`, `Secure` under TLS),
  set by login.
- CSRF: every authenticated non-GET request must send `X-CSRF-Token: <csrf_token>` (returned by
  login and `/auth/me`). In addition, all cross-origin state-changing requests are rejected
  (`Sec-Fetch-Site`/`Origin` check) with `403` plain text.
- All API responses carry `Cache-Control: no-store`.

### Errors

```json
{ "error": { "code": "validation_failed", "message": "must be 3 to 32 characters", "field": "username" } }
```

`field` is present only for `validation_failed`.

| Status | `code` | Meaning |
|---|---|---|
| 400 | `invalid_json` | Body is not one valid JSON object, or has unknown fields |
| 401 | `unauthenticated` | Missing, expired or revoked session |
| 401 | `invalid_credentials` | Wrong username/password (never says which) |
| 403 | `csrf_invalid` | Missing or wrong `X-CSRF-Token` |
| 403 | `setup_token_invalid` | Wrong setup token |
| 404 | `not_found` | Unknown endpoint or resource |
| 409 | `setup_completed` | Setup already done |
| 409 | `server_running` | The server must be stopped (memory/CPU limits, restore) |
| 409 | `server_busy` | The server is starting or restarting; retry shortly |
| 409 | `backup_in_progress` | A backup or restore of this server is in progress |
| 409 | `backup_not_ready` | The backup is still being made, or failed |
| 409 | `container_missing` | The server's container no longer exists; only deletion works |
| 409 | `container_not_managed` | The container no longer carries this server's labels; the panel refuses to touch it |
| 409 | `port_in_use` | The game port is taken on the host when starting |
| 413 | `payload_too_large` | Body over 64 KiB |
| 422 | `validation_failed` | Field-level validation error |
| 429 | `rate_limited` | Too many failed logins; retry after the window |
| 500 | `internal_error` | Unexpected server error (details only in logs) |
| 502 | `docker_error` | Docker rejected the operation (details only in logs) |
| 503 | `docker_unavailable` | The Docker daemon cannot be reached |
| 503 | `installer_unavailable` | An official version source (Mojang, Fabric, Forge, NeoForge) cannot be reached |

## Objects

**User**
```json
{ "id": "string", "username": "admin", "role": "admin", "created_at": "2026-11-17T12:00:00Z" }
```

**Session**
```json
{ "user": { /* User */ }, "csrf_token": "string" }
```

## Endpoints

### `GET /health` — public
`200 {"status":"ok"}`

### `GET /setup` — public
`200 {"required": true|false}`

### `POST /setup` — public, only while setup is required
Body: `{"token": "...", "username": "...", "password": "..."}`.
The one-time token is printed to the service log at startup while setup is pending.
Username: 3–32 chars of `[A-Za-z0-9._-]`, case-insensitive unique. Password: 12–256.
`201 {"user": User}` · `403 setup_token_invalid` · `409 setup_completed` · `422`.

### `POST /auth/login` — public
Body: `{"username": "...", "password": "..."}`.
`200 Session` + `Set-Cookie` · `401 invalid_credentials` · `429 rate_limited`.

### `POST /auth/logout` — auth + CSRF
`204`, clears the cookie and deletes the session.

### `GET /auth/me` — auth
`200 Session`.

### `POST /auth/password` — auth + CSRF
Body: `{"current_password": "...", "new_password": "..."}`.
`204`; every other session of the user is revoked · `401 invalid_credentials` · `422`.

### `GET /system/stats` — auth
Cached for 2 s. Network totals exclude `lo`, `veth*`, `docker*`, `br-*`, `virbr*`.
```json
{
  "collected_at": "2026-11-17T12:00:00.123Z",
  "cpu": { "cores": 12, "usage_percent": 7.5 },
  "memory": { "total_bytes": 0, "used_bytes": 0, "available_bytes": 0 },
  "disk": { "path": "/var/lib/mesquita-panel", "total_bytes": 0, "used_bytes": 0, "free_bytes": 0 },
  "network": { "rx_bytes": 0, "tx_bytes": 0, "rx_bytes_per_second": 0, "tx_bytes_per_second": 0 }
}
```

### `GET /system/containers` — auth
Metrics of containers labelled `mesquita.managed=true` only (other containers on the host are
never listed). Cached for 2 s. Containers that are not running are listed with zero metrics.
`cpu_percent` uses 100 per core (a container using two full cores reports 200). CPU and network
rates are computed between samples, so the first sample of a container reports 0.
`memory.used_bytes` excludes reclaimable page cache, like `docker stats`.
`server_id` is the `mesquita.server_id` label (empty string when absent).
`containers` is always an array (empty when there are none).
```json
{
  "collected_at": "2026-11-17T12:00:00.123Z",
  "containers": [
    {
      "id": "64-char hex", "name": "mesquita-srv-1", "server_id": "1", "state": "running",
      "cpu_percent": 12.5,
      "memory": { "used_bytes": 0, "limit_bytes": 0 },
      "network": { "rx_bytes": 0, "tx_bytes": 0, "rx_bytes_per_second": 0, "tx_bytes_per_second": 0 },
      "block_io": { "read_bytes": 0, "write_bytes": 0 }
    }
  ]
}
```
Errors: `503 docker_unavailable` when the Docker daemon cannot be reached.

### `GET /dashboard` — auth
```json
{
  "docker": { "reachable": true },
  "servers": { "total": 2, "running": 1, "stopped": 1 },
  "recent_events": [
    { "id": 2, "created_at": "...", "action": "auth.login", "target": "admin", "ip": "127.0.0.1", "success": true }
  ]
}
```
`servers.running` counts running, starting and restarting servers; servers whose container
is missing, or whose status is unknown, are only in `total`.
Audit actions: `auth.setup`, `auth.login`, `auth.logout`, `auth.password_change`,
`server.create`, `server.update`, `server.start`, `server.stop`, `server.restart`,
`server.kill`, `server.delete` (target: server name).

## Servers

Game servers run in Docker containers labelled `mesquita.managed=true` and
`mesquita.server_id=<id>`. Before acting on a container the panel checks both labels against
its database; other containers on the host are never listed or touched.

**Server**
```json
{
  "id": "26 characters [A-Z2-7]", "name": "Survival", "description": "",
  "game": "minecraft", "loader": "forge", "version": "1.20.1", "loader_version": "47.3.0",
  "status": "running",
  "memory_mb": 2048, "cpu_cores": 1.5, "port": 25601,
  "container_id": "64-char hex", "created_at": "...", "updated_at": "..."
}
```
`status` is read from Docker on every request, never stored:
`running`, `starting` (running, health check not passed yet), `stopped`, `restarting`,
`missing` (container gone or no longer labelled for this server), `unknown` (Docker unreachable).
`cpu_cores` is a number (0.5 = half a core); it is stored with 0.001-core precision.
`loader` is `vanilla`, `fabric`, `forge` or `neoforge`. `version` is the Minecraft release
(`"LATEST"` only for vanilla: it follows the newest release on every start). `loader_version`
is the exact loader build installed (empty for vanilla); it never changes by itself.

An `{id}` that is not a valid server ID answers `404 not_found` without any lookup.

### `GET /servers` — auth
`200 {"servers": [Server]}`, ordered by name. Always an array. When Docker is unreachable the
list is still returned, with status `unknown`.

### `POST /servers` — auth + CSRF
Body: `{"name", "description"?, "loader"?, "version"?, "loader_version"?, "memory_mb", "cpu_cores", "port"?, "accept_eula"}`.
- `name`: 1–48 printable characters, no surrounding spaces, unique (case-insensitive).
- `description`: up to 500 characters.
- `loader`: `vanilla` (default), `fabric`, `forge` or `neoforge`.
- `version`: vanilla: `"LATEST"` (default) or a release such as `"1.21.4"`. Modded: required,
  a release the loader supports (see Installers).
- `loader_version`: modded only; a build listed for that release. Omitted: the recommended build
  is chosen and stored.
- `memory_mb`: 512–16384. `cpu_cores`: 0.5 up to the number of host CPUs.
- `port`: within the configured game port range; omitted or `0` picks the first free one.
- `accept_eula`: must be `true` (the Minecraft EULA).

Modded servers are checked against the official version lists (cached for 1 hour). Pulls the
pinned game image for the release's Java version if needed (the first creation can take
minutes), creates the data directory and a **stopped** container. Forge and NeoForge run their
installer on the first start. On failure nothing is left behind.
`201 Server` · `422` (`field` names the input) · `502 docker_error` · `503 docker_unavailable` ·
`503 installer_unavailable` (modded only).

### `GET /servers/{id}` — auth
`200 Server` · `404`.

### `PATCH /servers/{id}` — auth + CSRF
Body: any of `{"name", "description", "memory_mb", "cpu_cores"}`. Name and description can
change at any time; limits only while the server is stopped. A new memory limit sets the heap
on the next start.
`200 Server` · `404` · `409 server_running` · `409 container_missing` · `422`.

### `POST /servers/{id}/start|stop|restart|kill` — auth + CSRF
`stop` and `restart` ask the game to save and stop, and kill it after 60 s. `kill` stops it
immediately (unsaved changes are lost). Starting a running server or stopping/killing a
stopped one is not an error.
`200 Server` · `404` · `409 container_missing` · `409 container_not_managed` ·
`409 port_in_use` (start) · `502` · `503`.

### `DELETE /servers/{id}?delete_files=true|false` — auth + CSRF
Stops the server gracefully, removes its container, then deletes its files and backups
(`delete_files=true`) or moves them to `servers/.deleted/<id>-<unix time>` and
`backups/.deleted/<id>-<unix time>` (default), and removes the record. A container that is already gone, or that no longer carries this server's
labels, is left alone and only forgotten.
`204` · `404` · `422` (invalid `delete_files`) · `502` · `503`.

## Installers

Version lists read from the official sources (Mojang, Fabric meta, Forge and NeoForge maven),
cached for 1 hour. A source that cannot be reached answers `503 installer_unavailable`.

### `GET /installers/{loader}/versions` — auth
`200 {"versions": ["1.21.1", "1.20.1", …]}`: the Minecraft releases the loader supports, newest
first (no snapshots). For `vanilla` the list starts with `"LATEST"`. Always an array.
`404` unknown loader · `503 installer_unavailable`.

### `GET /installers/{loader}/versions/{minecraft}` — auth
`200 {"loader_versions": ["47.4.5", "47.3.0", …], "recommended": "47.3.0"}`, newest first.
Recommended: Fabric's newest stable loader; Forge's promoted build (`recommended`, else
`latest`); NeoForge's newest non-beta build (else the newest).
`404` vanilla, unknown loader or release not supported · `503 installer_unavailable`.

## Files

The file manager of a server. Every operation is confined to the server's directory
(`<data dir>/servers/<id>`): paths are relative to it, `/`-separated, and `""` or `/` is its
root. A path with `..`, a backslash, NUL or control characters, longer than 1024 bytes or with
a segment over 255 bytes answers `422 validation_failed` (`field`: `path`, `from`, `to` or
`destination`). Symbolic links are listed but never followed outside the server directory
(`422`); they cannot be edited, and zip downloads and extractions skip them.

Writes are allowed while the server runs (the game may overwrite configuration files, and
changing world files while it runs can corrupt them). Uploads and edits are atomic: written to
a temporary file next to the target, then renamed. Files the panel creates belong to the user
the game container runs as. Every change is recorded in the audit log (`file.upload`,
`file.write`, `file.mkdir`, `file.rename`, `file.delete`, `file.extract`; target
`<server name>: /<path>`).

Transfers have no total time limit; a download or upload that makes no progress for 60 s is
dropped.

**FileEntry**
```json
{"name": "server.properties", "type": "file", "size": 1234, "modified_at": "2026-11-17T12:00:00Z"}
```
`type`: `file`, `dir` (size 0), `symlink` (size of the link itself) or `other` (devices,
sockets; never opened).

Errors common to these endpoints: `404 not_found` (server or path) · `409 not_a_directory` ·
`409 is_a_directory` · `422 validation_failed` · `507 insufficient_storage`.

### `GET /servers/{id}/files?path=<dir>` — auth
`200 {"path": "world", "entries": [FileEntry], "truncated": false}`. Directories first, then
by name. At most 10 000 entries; `truncated` is `true` when there are more.

### `GET /servers/{id}/files/download?path=<path>` — auth
A file is sent as `application/octet-stream` with `Content-Length`; a directory as a zip archive
(`application/zip`, entries prefixed with the directory's name, or `server/` for the root;
symlinks skipped; files changing during the download may be captured inconsistently). Both with
`Content-Disposition: attachment`. An error after the transfer started aborts the connection.

### `GET /servers/{id}/files/content?path=<file>` — auth
For the editor: `200 {"path", "content", "version", "size"}`. `version` is opaque.
`413 file_too_large` (over 1 MiB) · `415 not_text` (not UTF-8, or contains NUL) ·
`422` (symlink, special file or root).

### `PUT /servers/{id}/files/content` — auth + CSRF
Body `{"path", "content", "version"?}` (body up to ~6 MiB; `content` up to 1 MiB). Creates or
replaces the file; an existing file keeps its permissions. With `version`, the file must still
be at that version, else `409 file_changed` (also when it was deleted). Without `version` the
file is written unconditionally.
`200 {"version"}` · `409 file_changed` · `413 file_too_large`.

### `POST /servers/{id}/files/upload?path=<dir>&overwrite=true|false` — auth + CSRF
`multipart/form-data`; every part with a file name is saved in `<dir>` under its base name
(other fields are ignored). Streamed to disk, up to 2 GiB per request. Without
`overwrite=true` an existing file answers `409 file_exists` (files saved earlier in the same
request are kept).
`201 {"files": [FileEntry]}` · `400 invalid_multipart` · `409 file_exists` ·
`413 payload_too_large` · `422` (no file, invalid name, invalid `overwrite`).

### `POST /servers/{id}/files/directory` — auth + CSRF
Body `{"path"}`; the parent must exist. `204` · `409 file_exists`.

### `POST /servers/{id}/files/rename` — auth + CSRF
Body `{"from", "to"}`; also moves between directories. An existing `to` is never replaced
(`409 file_exists`); a directory cannot move into itself (`422`). `204`.

### `POST /servers/{id}/files/extract` — auth + CSRF
Body `{"path": "<zip file>", "destination": "<dir>", "overwrite"?: false}`. Entries are
validated like any path (an entry with `..` or an absolute name rejects the whole archive);
names without the zip UTF-8 flag are decoded as CP437, as the format specifies (so a byte
`0x15` becomes `§`, common in Minecraft world names); symlinks are skipped. Limits: 100 000 entries and 8 GiB actually decompressed. Conflicts are
checked before anything is written; a corrupt entry found midway leaves the entries already
written.
`200 {"entries": n}` · `409 file_exists` · `413 archive_too_large` · `422 invalid_archive`.

### Modpack scripts

Modpacks are imported through the existing file upload and ZIP extraction endpoints. Upload the
server-pack ZIP into the server root, then extract it into the root (overwrite is disabled by
default). The panel discovers regular `.sh` files inside the server directory; symlinks are
not followed. Create the server with the modpack's exact Minecraft version first because that
version selects the Java image used to run its scripts.

`GET /servers/{id}/modpack` returns `{ "supported", "scripts", "install_script", "start_script", "installed" }`.
`PUT /servers/{id}/modpack` accepts `{ "install_script", "start_script" }`; both paths must
refer to discovered shell scripts and the server must be stopped. The chosen paths are stored
as server metadata inside the data directory and included in backups.

`POST /servers/{id}/modpack/install` starts the managed container. On its first start it runs
the selected installer as the container's unprivileged server user; after success it records
completion and immediately runs the selected startup script. Later starts run only the startup
script. Installer and game output use the existing Console stream. A failed installer does not
mark installation complete; the container stays idle to avoid an automatic restart loop. Stop
it, then start installation again to retry. This feature is available for servers
created after the modpack-capable container wrapper is installed; older containers remain
unchanged and report `supported: false`.

Scripts are executable code from the uploaded pack. The panel runs them only inside its
managed, resource-limited, non-root container; installation is an explicit user action.

### `DELETE /servers/{id}/files?path=<path>` — auth + CSRF
Deletes a file, a symlink (never its target) or a directory with everything inside. The root
cannot be deleted (`422`). `204`.

## Backups

A backup is a zip archive of the server's whole directory (directories and regular files;
symlinks and the panel's temporary files are skipped), stored in
`<data dir>/backups/<server id>/<backup id>.zip`, outside the file manager's reach. Backups
live on the same disk as the server: download them to keep a copy elsewhere.

- **Running server:** the panel sends `save-off` and `save-all flush` to the game console and
  waits (up to 60 s) for the game's `Saved the game`; the archive is written with saving
  paused and `save-on` is always sent afterwards. Players are told in the chat (gray
  `tellraw @a`): `[Backup] Starting server backup...`, then `[Backup] Backup complete.` or
  `[Backup] Backup failed; automatic saving is back on.`; a message that cannot be sent never
  fails the backup. These commands appear in the audit log as `server.console_command`.
- **Stopped server** (or its container is missing): archived directly. If the server is
  started during the backup, the backup fails (`server_busy`).
- A starting or restarting server answers `409 server_busy`; without Docker, `503`.
- One backup or restore per server at a time (`409 backup_in_progress`), and one archive is
  written at a time across all servers (others wait as `creating`).
- Disk reserve: a backup is refused (`507 insufficient_storage`) unless the free space minus
  2 GiB is at least the size of the server's files; a restore may decompress at most the free
  space minus 2 GiB.

Audit: `backup.create`, `backup.restore`, `backup.delete`, `backup.schedule_update` (target
`<server name>: <backup id>`, or the server name for the schedule). Scheduled backups and
retention are recorded without a user.

**Backup**
```json
{"id": "string", "kind": "manual", "status": "ready", "size_bytes": 123456, "failure": "",
 "created_at": "2026-11-17T12:00:00Z", "completed_at": "2026-11-17T12:01:30Z"}
```
`kind`: `manual` or `scheduled`. `status`: `creating` (queued or being written; `size_bytes`
0, `completed_at` null), `ready`, or `failed` (no archive). `failure`, set when failed:
`interrupted` (the panel stopped meanwhile), `save_timeout` (the game did not confirm its
save), `server_busy`, `insufficient_storage`, `docker_unavailable` or `error` (details in the
panel log).

**BackupSchedule**
```json
{"interval_hours": 24, "retain": 7, "next_run_at": "2026-11-18T12:00:00Z"}
```
`interval_hours`: 0 (off) to 720. `retain`: how many scheduled backups to keep, 0 (all) to
100; after each successful scheduled backup the older scheduled ones beyond `retain` and all
failed scheduled ones are deleted. Manual backups are never deleted automatically.
`next_run_at` is null while off. A due backup of a busy server (another backup, a restore,
starting) is retried every minute; any other refusal (e.g. no disk space) is stored as a failed
scheduled backup and the next run is one interval later. After the panel was down for longer
than an interval, one backup runs at startup.

Errors common to these endpoints: `404 not_found` (server or backup) · `503 docker_unavailable`.

### `GET /servers/{id}/backups` — auth
`200 {"backups": [Backup]}`, newest first.

### `POST /servers/{id}/backups` — auth + CSRF
Starts a manual backup. No body. Answers at once with `202 Backup` (`status: creating`); poll
the list until it is `ready` or `failed`.
`409 server_busy` · `409 backup_in_progress` · `507 insufficient_storage`.

### `GET /servers/{id}/backups/{backupID}/download` — auth
The archive as `application/zip` with `Content-Length` and
`Content-Disposition: attachment; filename=<server name>-<YYYYMMDD-HHMMSS>.zip` (UTC). An
error after the transfer started aborts the connection. `409 backup_not_ready`.

### `POST /servers/{id}/backups/{backupID}/restore` — auth + CSRF
Replaces the server's files with the backup; the server must be stopped. The backup is first
extracted next to the server; only then the current directory is moved to
`servers/.deleted/<id>-<unix time>-pre-restore` (recoverable by hand) and the restored one put
in its place, so a failure or a cancelled request leaves the server as it was. If the server
was started during the extraction, nothing is replaced (`409 server_running`). Restored files
belong to the game container's user, with modes 0640/0750. Can take minutes (no response
until done; the request may run up to 30 min).
`204` · `409 server_running` · `409 backup_not_ready` · `409 backup_in_progress` ·
`422 invalid_archive` · `507 insufficient_storage`.

### `DELETE /servers/{id}/backups/{backupID}` — auth + CSRF
Deletes the backup and its archive. `204` · `409 backup_in_progress` (being made, or a restore
of this server is running).

### `GET /servers/{id}/backups/schedule` — auth
`200 BackupSchedule` (off for a server that never had one).

### `PUT /servers/{id}/backups/schedule` — auth + CSRF
Body `{"interval_hours", "retain"}` (both required). The next run is one interval from now;
when only `retain` changes, the planned run is kept.
`200 BackupSchedule` · `422 validation_failed` (`interval_hours`, `retain`).

## Players

Whitelist and ban lists of a Minecraft server. The lists are read from the game's files
(`whitelist.json`, `banned-players.json`, `banned-ips.json`, and `white-list` in
`server.properties`), so they show what is on disk.

- **Running (or starting) server:** every change is sent to the game console (`whitelist
  on|off|add|remove`, `ban`, `pardon`, `ban-ip`, `pardon-ip`), because the running game owns
  those files and rewrites them. The game resolves UUIDs; it may write its files a moment
  after the `204`. The commands also appear as `server.console_command` in the audit log.
- **Stopped server (or missing container):** the files are edited directly. The whitelist
  switch sets `white-list` and `enforce-whitelist` in `server.properties`, keeping its other
  lines. Adding a player or a player ban needs a UUID: only offline-mode servers
  (`online-mode=false` in `server.properties`) get one computed by the panel; otherwise
  `422 validation_failed` (field `name`) asks for the server to be started. Removals and IP
  bans never need a UUID.
- Restarting servers answer `409 server_busy`; without Docker, `503 docker_unavailable`.
- A list file that is not a valid JSON array is never overwritten: `422 validation_failed`
  (field is the file name).
- Removing something that is not on the list succeeds (idempotent). Names are matched
  case-insensitively.

Audit: `whitelist.toggle` (target `<server name>: on|off`), `whitelist.add`,
`whitelist.remove`, `player.ban`, `player.pardon`, `ip.ban`, `ip.pardon` (target
`<server name>: <player name or IP>`).

### `GET /servers/{id}/players` — auth
`200 {"whitelist": {"enabled": bool, "players": [{"uuid", "name"}]}, "banned_players":
[{"uuid", "name", "created", "source", "expires", "reason"}], "banned_ips": [{"ip",
"created", "source", "expires", "reason"}]}`. `created`, `source` and `expires` are the
game's own strings (`expires` is `forever` for panel-made bans).

### `PUT /servers/{id}/players/whitelist` — auth + CSRF
Body `{"enabled": bool}`. `204`.

### `POST` / `DELETE /servers/{id}/players/whitelist` — auth + CSRF
Body `{"name"}`: 1 to 16 letters, digits or underscores. `204` · `422 validation_failed`.

### `POST` / `DELETE /servers/{id}/players/bans` — auth + CSRF
Body `{"name", "reason"}` (`reason` optional, at most 200 characters, one line, ignored by
`DELETE`). `204` · `422 validation_failed`.

### `POST` / `DELETE /servers/{id}/players/ip-bans` — auth + CSRF
Body `{"ip", "reason"}` (IPv4 or IPv6; `reason` as above). `204` · `422 validation_failed`.

## WebSocket

### `GET /ws` — auth
Live console and server status. Protocol, messages, limits and close codes:
[`WEBSOCKET.md`](WEBSOCKET.md). Handshake errors: `400 bad_handshake` ·
`401 unauthenticated` · `403 origin_not_allowed` · `429 too_many_connections` ·
`503 shutting_down`.
