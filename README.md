<div align="center">

<img src="web/assets/icons/logo.svg" alt="Craftkeeper logo" width="96" height="96">

# Craftkeeper

**A lightweight, self-hosted panel for Minecraft servers running in Docker.**
One static Go binary. No Node, no CDN, works offline.

[English](README.md) · [Português](README.pt-BR.md)

![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-Engine%20API-2496ED?logo=docker&logoColor=white)
![SQLite](https://img.shields.io/badge/SQLite-pure%20Go-003B57?logo=sqlite&logoColor=white)
![Tailwind CSS](https://img.shields.io/badge/Tailwind%20CSS-v4-06B6D4?logo=tailwindcss&logoColor=white)
![Platform](https://img.shields.io/badge/platform-linux%2Famd64-informational?logo=linux&logoColor=white)

</div>

Craftkeeper manages game servers as Docker containers on the host it runs on. The backend is
Go, the frontend is plain HTML, ES modules and Web Components with Tailwind CSS v4, and
everything is embedded in a single ~14 MB binary. It is an alternative to
[Crafty Controller](https://craftycontrol.com/) and [Pterodactyl](https://pterodactyl.io/)
for people who want something small enough to read and audit.

> **Naming note:** the product is Craftkeeper, but the Go binary, systemd unit, paths and
> container labels still use the original name `mesquita-panel` / `mesquita.managed`.

## Screenshots

<!--
Drop images in docs/images/ and uncomment:

![Overview](docs/images/overview.png)
![Console](docs/images/console.png)
![Mods](docs/images/mods.png)
![Files](docs/images/files.png)
-->

_Coming soon._

## Features

**Servers**
- Create servers in the background: Vanilla, Fabric, Forge and NeoForge, with versions read
  from the official sources (Mojang, Fabric, Forge, NeoForge) and pinned at creation.
- The right Java image per Minecraft version, pinned by digest.
- Change Minecraft version or loader (with a safety backup), set RAM and CPU limits.
- Crash-loop detection: a server stuck restarting is stopped and flagged `crashed`, with the
  latest crash report one click away.

**Console and activity**
- Live console over WebSocket with persistent command history, search and old `logs/*.log.gz`.
- Audit log of every administrative action, global and per server.

**Mods and modpacks**
- Browse and install mods from [Modrinth](https://modrinth.com/) filtered by loader and
  Minecraft version, with dependency handling, SHA-512 verification, enable/disable and
  updates with an optional backup.
- Import a server-pack ZIP when creating a server.

**Players and worlds**
- Whitelist, bans, IP bans and operators; see who is online right now.
- Reset a world (with seed), import one, or download it.

**Files and backups**
- File manager with streaming uploads (up to 10 GiB), editor, zip and unzip, confined to the
  server folder with `os.Root` (no traversal, no symlink escape).
- Online backups (`save-off` / `save-all flush`), restore, scheduled backups with retention.

**Security first**
- Argon2id passwords, server-side sessions, CSRF on every mutating request, login rate limit,
  one-time setup token, audit trail.
- Never binds `0.0.0.0` by default; acts only on containers it created
  (`mesquita.managed=true` + known `server_id`); game containers run non-root with no
  capabilities. See [Security](docs/SECURITY.md).

## Architecture at a glance

Clean Architecture organised by layer; dependencies point inwards.

```
cmd/panel/main.go                 composition root
internal/domain/                  entities, rules, errors (no infrastructure imports)
internal/application/<feature>/   use cases + the ports they consume
internal/infrastructure/<tech>/   docker, sqlite, filesystem, modrinth, ...
internal/presentation/http/       REST handlers, routing, middleware
internal/presentation/websocket/  console and status transport
migrations/                       versioned SQL, embedded
web/                              embedded frontend
```

Docker is driven through the Engine API over its unix socket with plain `net/http` (no SDK).
More in [Architecture](docs/ARCHITECTURE.md).

## Quick start (development)

Go is not required on the host; builds run in a pinned `golang` container.

```bash
sudo make check     # gofmt + vet + tests + build (output in .cache/last-run.log)
make run            # http://127.0.0.1:8090, data in ./.data
make css            # regenerate web/assets/css/app.css after changing classes
```

On first start the log prints a one-time setup token; use it to create the administrator.

## Deployment

Craftkeeper runs as a systemd service behind loopback, optionally with HTTPS through
`tailscale serve`. Step-by-step guide: [Installation](docs/INSTALL.md). All environment
variables: [Configuration](docs/CONFIGURATION.md).

## Status

| Phase | Scope | Status |
|---|---|---|
| 1–7 | Foundation, monitoring, server management, console, file manager, backups, game installers | ✅ implemented and tested |
| 8 | Crash-loop guard | ✅ verified with a real server; systemd deploy pending |
| 9 | Console history/search/old logs, activity log | ✅ done |
| 11 | Version/loader change, worlds, operators | ✅ done |
| 12 | Mods via Modrinth | 🚧 implemented, real-server check pending |
| 13–14 | Metrics history, global backups page, clone/templates | 📋 planned |

Detailed log and decisions: [`MEMORY.md`](MEMORY.md).

## Documentation

| | |
|---|---|
| [Specification](docs/SPEC.md) | Requirements, source of truth |
| [Architecture](docs/ARCHITECTURE.md) | Layers, components, Docker policy |
| [Security](docs/SECURITY.md) | Threat model and controls |
| [Development](docs/DEVELOPMENT.md) | Build, test, conventions |
| [Installation](docs/INSTALL.md) | Ubuntu + systemd |
| [Configuration](docs/CONFIGURATION.md) | Environment variables and fixed limits |
| [REST API](docs/API.md) | `/api/v1` reference |
| [WebSocket protocol](docs/WEBSOCKET.md) | Versioned message format |
