# Mesquita Panel

Lightweight, self-hosted game server management panel — a single static Go binary with an
embedded HTML / Vanilla JS / Tailwind CSS v4 frontend. Inspired by Crafty Controller and
Pterodactyl, built for one Ubuntu host first.

## Status

| Phase | Scope | Status |
|---|---|---|
| 1 | Foundation: config, SQLite + migrations, auth (setup, login, sessions, CSRF, rate limit, password change, audit), HTTP server, embedded UI, host metrics, systemd | implemented, tested (not yet deployed) |
| 2–5 | Monitoring, server management, WebSocket console, file manager | implemented, tested |
| 6 | Backups (online, download, restore, schedule + retention) | implemented, tested |
| 7 | Game installers: Vanilla, Fabric, Forge, NeoForge with pinned versions from the official sources; Java image per Minecraft version | implemented, tested |

Details: [`MEMORY.md`](MEMORY.md). Full specification: [`docs/SPEC.md`](docs/SPEC.md).

## Quick start (development)

Go is not required on the host; builds run in a pinned `golang` container.

```bash
sudo make check     # gofmt + vet + tests + build (output in .cache/last-run.log)
make run            # http://127.0.0.1:8090, data in ./.data
make css            # regenerate web/assets/css/app.css after changing classes
```

## Documentation

- [Architecture](docs/ARCHITECTURE.md)
- [Security](docs/SECURITY.md)
- [Development](docs/DEVELOPMENT.md)
- [Installation](docs/INSTALL.md)
- [Configuration](docs/CONFIGURATION.md)
- [REST API](docs/API.md)
- [WebSocket protocol](docs/WEBSOCKET.md)
