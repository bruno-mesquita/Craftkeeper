# Development

## Requirements

- Docker, to build and test (Go is **not** installed on the host; D1).
- `make`, `curl`, `sha256sum`.
- Nothing else: no Node, npm or Go toolchain on the host.

The pinned versions are at the top of the `Makefile`: Go image `golang:1.27.4-trixie`,
Tailwind CLI v4.3.5 (with its SHA-256).

## Commands

| Command | What it does |
|---|---|
| `sudo make check` | gofmt check + `go vet` + `go test ./...` + build — run this before declaring work done |
| `sudo make build` | `bin/craftkeeper` (linux/amd64, static, `-trimpath -ldflags="-s -w"`) |
| `sudo make test` / `sudo make vet` | tests or vet only |
| `sudo make fmt` | `gofmt -l -w` on `cmd internal migrations web` |
| `sudo make tidy` | `go mod tidy` |
| `make css` | regenerate `web/assets/css/app.css` with the standalone Tailwind CLI (downloaded once to `tools/`, checksum-verified) |
| `make run` | run `bin/craftkeeper` on `127.0.0.1:8090` with data in `./.data` and debug logs |
| `make clean` | remove `bin/` and `.data/` |
| `sudo make deploy` | upgrade the installed systemd service with backup, health check and rollback ([INSTALL.md](INSTALL.md#upgrade)) |

Go targets run inside a throwaway `golang` container as the invoking user (`SUDO_UID` /
`SUDO_GID`), with module and build caches in `.cache/`. Their output is also written to
`.cache/last-run.log`.

`sudo` is needed only because the development user has no access to the Docker socket. Running
the binary (`make run`) does not need it, unless you want to manage real containers:
`sudo make run`.

To reach a dev instance from another machine on the tailnet:

```bash
make run RUN_LISTEN=127.0.0.1:8090,100.126.54.17:8090
```

## Layout and rules

See [`ARCHITECTURE.md`](ARCHITECTURE.md). In short:

- Code by layer: `domain`, `application/<feature>`, `infrastructure/<tech>`, `presentation`.
- Interfaces in the consuming package; constructors for dependencies; no globals.
- Code, identifiers, comments and `docs/` in English.
- Frontend: plain ES modules and DOM helpers, no framework, no bundler, no CDN. Changing Tailwind
  classes requires `make css` and committing `app.css`.

## Changing contracts

- **REST** — update [`API.md`](API.md) and the HTTP contract tests in
  `internal/presentation/http/*_test.go`. Errors use `{"error":{"code","message"}}`.
- **WebSocket** — update [`WEBSOCKET.md`](WEBSOCKET.md) and the hub tests.
- **Database** — add a new `migrations/NNNN_description.sql`. Never edit an applied migration.
  Add a test that the migration applies on top of an older database.
- **Configuration** — update [`CONFIGURATION.md`](CONFIGURATION.md) and
  `deployments/config.env.example`.

## Tests

`go test ./...` covers:

- domain rules and validation;
- application services with in-memory fakes of their ports;
- adapters against fakes of the outside world: a fake Docker daemon on a unix socket, temporary
  directories for the filesystem (traversal, symlinks, zip slip), `httptest` servers with
  recorded responses for the version sources, a temporary SQLite database;
- HTTP and WebSocket contracts end to end through the real handler.

Tests never touch real containers. Integration tests that need Docker must create containers
labelled `craftkeeper.managed=true` with names starting with `craftkeeper-test-`, and remove only
what they created.

The backup HTTP tests use the container's real `/tmp` and need more than 2 GiB free there
(the disk reserve).

## Production host rules

This repository lives on the production server (`mesquita-server`), next to Crafty and live
Minecraft servers. Without explicit authorization do **not**: touch Crafty or any container the
panel did not create, read or change `/home/bruno-mesquita/docker/`, install packages, edit
`/etc`, systemd, firewall or Tailscale, or bind to ports already in use. See `CLAUDE.md` and
`MEMORY.md` for the full list and the host facts.

## Manual verification

Every phase ends with a real run (`sudo make run`) against real containers, recorded in
`MEMORY.md`. Before testing the UI on a clean slate, run `make clean` to drop the local test
database.
