# Architecture

Craftkeeper is one static Linux binary: a Go HTTP server with an embedded HTML + Vanilla JS
frontend. It manages game servers as Docker containers on the same host and keeps its own
state in SQLite. Requirements live in [`SPEC.md`](SPEC.md); decisions, with their reasons, in
[`../MEMORY.md`](../MEMORY.md) (`D1`–`D45`).

## Layers

Clean Architecture organised **by layer**, with features as subpackages inside each layer
(D8):

```
cmd/panel/main.go                 composition root: config, adapters, services, HTTP server
internal/domain/                  entities, value rules, errors; imports no infrastructure
internal/application/<feature>/   use cases; each defines the ports (interfaces) it consumes
internal/infrastructure/<tech>/   adapters that implement those ports
internal/presentation/http/       REST handlers, routing, middleware, JSON
internal/presentation/websocket/  WebSocket hub: connections, subscriptions, versioned messages
internal/config/                  environment parsing and validation
migrations/                       versioned SQL, embedded
web/                              embedded frontend (index.html, assets/)
```

Dependencies point inwards: `domain` ← `application` ← `infrastructure` / `presentation`.
Presentation never calls infrastructure directly. `main.go` is the only place that knows every
concrete type; everything else receives its dependencies through constructors. There is no
global mutable state and no `init()` with side effects.

Interfaces are small and live in the consuming package (for example
`application/servers/ports.go`). An interface exists only for a real boundary: the container
runtime, repositories, version sources and the WebSocket adapter.

## Components

| Feature | Application | Infrastructure | Presentation |
|---|---|---|---|
| Auth, sessions, setup | `auth` | `sqlite` (users, sessions), `password` (Argon2id) | `http` |
| Host and container metrics | `monitoring` | `metrics` (`/proc`, `statfs`), `docker` (stats) | `http` |
| Dashboard | `dashboard` | `docker`, `sqlite` (audit) | `http` |
| Servers (lifecycle, limits) | `servers` | `docker` (runtime), `sqlite`, `filesystem` (dirs), `hostnet` | `http` |
| Installers (versions) | `installers` | `gameversions` (official sources over HTTPS) | `http` |
| Console and live status | `console` | `docker` (logs, attach, events) | `websocket` |
| File manager | `files` | `filesystem` (`os.Root`, zip) | `http` |
| Backups | `backups` | `filesystem` (zip, staging), `sqlite` | `http` |
| Mods (Modrinth) | `mods` | `modrinth` (API + CDN over HTTPS, SHA-512 check), `filesystem` | `http` |
| Shared short cache | `ttlcache` | — | — |

### Docker

`internal/infrastructure/docker` talks to the Engine API (`/v1.44`) over the unix socket with
`net/http`; there is no Docker SDK (D5, D16). Only allowlisted endpoints are used, and every
operation on an existing container first inspects it and checks the labels
`craftkeeper.managed=true` and `craftkeeper.server_id` against the database (D22). Containers the
panel did not create — Crafty and its servers — are never listed or touched.

Game servers run `itzg/minecraft-server`, one image per Java version, pinned by digest (D19,
D45). Container policy is fixed (D21): non-root user, no capabilities, `no-new-privileges`,
memory/CPU/PID limits, bounded logs, default bridge network.

### WebSocket

`internal/infrastructure/websocket` is the only package that imports `gorilla/websocket`
(D6). The hub in `presentation/websocket` owns connections, per-user limits, bounded send
queues, subscriptions and session revalidation (D28). Messages are versioned JSON, specified in
[`WEBSOCKET.md`](WEBSOCKET.md).

The console follows each server's container log through one shared feed per server, created
for its first subscriber and cancelled after its last (D26). Commands go to the game's stdin
through a short attach connection (D25). Status changes come from Docker events (D27).

### Persistence

SQLite through `modernc.org/sqlite` (pure Go, `CGO_ENABLED=0`), WAL mode, one writer
connection and a separate read pool (D4). Migrations in `migrations/NNNN_*.sql` are embedded,
applied in order inside transactions and are **append-only**. Times are stored as Unix
seconds and exposed as RFC 3339 UTC (D14). Server status is never stored: it is derived from
Docker on every read.

Server files live in `<data>/servers/<id>` and backups in `<data>/backups/<id>`; both are
reached only through `os.Root` (D30).

## Lifecycles

Long-lived goroutines have one owner and stop through `context` cancellation:

- `console.Service.Run` — Docker event loop for the process lifetime; log feeds per viewer.
- `backups.Service.Run` — schedule ticker (1 min); backup jobs tracked by a `WaitGroup`.
- WebSocket connections — reader and writer per connection, owned by the hub.

On shutdown (`SIGINT`/`SIGTERM`) `main.go` stops the HTTP servers, then the WebSocket hub,
then the backup jobs and the console, with a 10 s timeout. On start, `backups.Recover` marks
interrupted jobs as failed and removes temporary files before serving.

Queues and channels are bounded; files, archives, logs and backups are streamed, never loaded
whole into memory.

## Contracts

These are public and stable, so the backend can later be rewritten (for example in Rust)
without changing clients or data:

- REST JSON under `/api/v1` — [`API.md`](API.md).
- WebSocket messages — [`WEBSOCKET.md`](WEBSOCKET.md).
- SQLite schema — `migrations/`.
- Docker labels `craftkeeper.managed`, `craftkeeper.server_id`, `craftkeeper.game`.
- Marker files at the root of each server directory: `.craftkeeper-modpack`,
  `.craftkeeper-modpack-installed`, `.craftkeeper-modpack-supported`.

No Go-specific types or error names leak into them.

## Frontend

`web/index.html` loads ES modules from `web/assets/js` (no bundler, no framework): a small
hash router (`app.js`), pages in `pages/`, shared helpers (`api.js`, `dom.js`, `ws.js`,
`dialog.js`). Text is always inserted as text nodes. Styles are Tailwind CSS v4 compiled
ahead of time into `web/assets/css/app.css`, which is committed and embedded (D3). Everything
is served by the binary; nothing loads from a CDN.

## Dependencies

| Module | Why |
|---|---|
| `modernc.org/sqlite` | SQLite without CGO |
| `golang.org/x/crypto` | Argon2id |
| `github.com/gorilla/websocket` | WebSocket protocol (framing, handshake) |

Everything else is the standard library. New dependencies need a reason recorded in
`MEMORY.md`.
