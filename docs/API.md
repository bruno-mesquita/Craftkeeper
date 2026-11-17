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
| 409 | `server_running` | Memory/CPU limits can only change while the server is stopped |
| 409 | `container_missing` | The server's container no longer exists; only deletion works |
| 409 | `container_not_managed` | The container no longer carries this server's labels; the panel refuses to touch it |
| 409 | `port_in_use` | The game port is taken on the host when starting |
| 413 | `payload_too_large` | Body over 64 KiB |
| 422 | `validation_failed` | Field-level validation error |
| 429 | `rate_limited` | Too many failed logins; retry after the window |
| 500 | `internal_error` | Unexpected server error (details only in logs) |
| 502 | `docker_error` | Docker rejected the operation (details only in logs) |
| 503 | `docker_unavailable` | The Docker daemon cannot be reached |

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
  "game": "minecraft", "loader": "vanilla", "version": "LATEST",
  "status": "running",
  "memory_mb": 2048, "cpu_cores": 1.5, "port": 25601,
  "container_id": "64-char hex", "created_at": "...", "updated_at": "..."
}
```
`status` is read from Docker on every request, never stored:
`running`, `starting` (running, health check not passed yet), `stopped`, `restarting`,
`missing` (container gone or no longer labelled for this server), `unknown` (Docker unreachable).
`cpu_cores` is a number (0.5 = half a core); it is stored with 0.001-core precision.

An `{id}` that is not a valid server ID answers `404 not_found` without any lookup.

### `GET /servers` — auth
`200 {"servers": [Server]}`, ordered by name. Always an array. When Docker is unreachable the
list is still returned, with status `unknown`.

### `POST /servers` — auth + CSRF
Body: `{"name", "description"?, "version"?, "memory_mb", "cpu_cores", "port"?, "accept_eula"}`.
- `name`: 1–48 printable characters, no surrounding spaces, unique (case-insensitive).
- `description`: up to 500 characters.
- `version`: `"LATEST"` (default) or a release such as `"1.21.4"`.
- `memory_mb`: 512–16384. `cpu_cores`: 0.5 up to the number of host CPUs.
- `port`: within the configured game port range; omitted or `0` picks the first free one.
- `accept_eula`: must be `true` (the Minecraft EULA).

Pulls the pinned game image if needed (the first creation can take minutes), creates the data
directory and a **stopped** container. On failure nothing is left behind.
`201 Server` · `422` (`field` names the input) · `502 docker_error` · `503 docker_unavailable`.

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
Stops the server gracefully, removes its container, then deletes its files
(`delete_files=true`) or moves them to `servers/.deleted/<id>-<unix time>` (default), and
removes the record. A container that is already gone, or that no longer carries this server's
labels, is left alone and only forgotten.
`204` · `404` · `422` (invalid `delete_files`) · `502` · `503`.

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

### `DELETE /servers/{id}/files?path=<path>` — auth + CSRF
Deletes a file, a symlink (never its target) or a directory with everything inside. The root
cannot be deleted (`422`). `204`.

## WebSocket

### `GET /ws` — auth
Live console and server status. Protocol, messages, limits and close codes:
[`WEBSOCKET.md`](WEBSOCKET.md). Handshake errors: `400 bad_handshake` ·
`401 unauthenticated` · `403 origin_not_allowed` · `429 too_many_connections` ·
`503 shutting_down`.

## Planned

Backups (phase 6) — as listed in `docs/SPEC.md` §10.
