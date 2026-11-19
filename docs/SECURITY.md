# Security

The panel controls Docker on a production host, so access to it is close to root access on
that host. This document describes the threat model and the controls in place. Decision
numbers (`Dnn`) refer to [`../MEMORY.md`](../MEMORY.md).

## Threat model

- **Network exposure.** The panel listens only on explicit addresses: `127.0.0.1` and the
  Tailscale address by default. `0.0.0.0` is refused (D2). Game ports are public by design;
  the panel is not.
- **Docker is root-equivalent.** The service user needs the `docker` group to manage servers
  (D10). Anyone who controls the panel process controls the host. The panel therefore never
  passes shell commands to the host and acts only on containers it created.
- **Shared host.** Crafty and its Minecraft servers run on the same machine and must never be
  affected.
- **Game servers are untrusted.** Server files, mods and console output are treated as hostile
  input (symlinks, zip slip, oversized output).

## Authentication

- One administrator, created by the initial setup. Setup requires a one-time token printed in
  the service log at start, and is closed once an admin exists (D11).
- Passwords: 12–256 characters, Argon2id (m=19 MiB, t=2, p=1), at most 2 hashes at a time;
  verification rejects absurd stored parameters (D13).
- Login rate limit: 5 failures per username and 20 per IP in 15 minutes.
- Sessions: 128-bit random token in the cookie, only its SHA-256 is stored; 7-day absolute and
  24-hour idle expiry (D9). Cookie `HttpOnly`, `SameSite=Strict`, `Secure` under TLS.
- No default password; no password is ever logged.

## Request protection

- **CSRF**, two layers (D12): `http.CrossOriginProtection` (`Sec-Fetch-Site` / `Origin`) on the
  whole API, and an `X-CSRF-Token` tied to the session on every authenticated mutating request.
- **WebSocket** (D29): `Origin` must match `Host`; the session cookie is required and
  revalidated every 60 s and before each console command; per-user and global connection
  limits, 4 KiB messages, bounded queues, write deadlines (D28).
- **Input**: JSON bodies limited to 64 KiB with unknown fields rejected; every field validated
  in the domain; uploads, editor content, archives and listings have fixed limits (D33).
- **Headers**: `Content-Security-Policy` with `script-src 'self'` (no inline scripts) and
  `frame-ancestors 'none'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`,
  `X-Frame-Options: DENY`, `Cross-Origin-Opener-Policy: same-origin`, `Cache-Control: no-store`
  on the API.
- **Errors**: clients get a code and a generic message; Docker and internal details stay in the
  log.

## Docker

- Engine API through a small allowlist of endpoints; no SDK, no `exec`, no arbitrary commands
  (D5). Console input is written to the game's stdin (D25).
- Before acting on a container the panel inspects it and requires `mesquita.managed=true`
  **and** a `mesquita.server_id` that matches the database (D22). Metrics and lists are
  filtered by label in the daemon and checked again in the client (D17).
- A container that disappeared or changed labels is only forgotten, never touched (D23).
- Game containers: run as the panel's uid:gid (never root), `CapDrop ALL`, `no-new-privileges`,
  no swap, PID limit 2048, bounded logs, default bridge network (D21).
- Images are pinned by digest, so the daemon verifies their content (D19, D45).

## Filesystem

- Every server file operation goes through `os.Root` on that server's directory: `..`,
  absolute paths and symlinks that escape are rejected by the kernel-backed root (D30).
- Symlinks are listed but never followed out, not editable, and skipped by zip and extraction.
- Archive extraction rejects the whole zip on any unsafe entry and limits entries and real
  uncompressed bytes (D33).
- Writes are atomic (temporary file, `fsync`, rename) and keep the original mode (D31, D32).
- Backups are stored outside the file manager's reach; restore extracts into a staging
  directory first (D36, D40). A 2 GiB disk reserve is always kept (D39).

## Version sources

The installers read version lists from fixed official HTTPS endpoints (Mojang, Fabric, Forge,
NeoForge) with the host's CA store, a 15 s timeout and an 8 MiB limit. Every value is checked
against strict patterns before it reaches a container environment, and modded servers pin
exact versions so nothing changes on restart (D43, D44).

## Mods (Modrinth)

The mods service talks to `api.modrinth.com` and downloads only from `cdn.modrinth.com`
(redirects elsewhere are refused). Modrinth IDs, file names, hashes and sizes are validated
before use; each jar is at most 256 MiB, verified against its published SHA-512 and size while
it streams into a temporary file that is renamed into `mods/` only after the check (D32), and
2 GiB of disk stay free. Icons are proxied by the panel (raster formats only, no SVG, at most
256 KiB) so the CSP keeps `img-src 'self' data:`. Mods are third-party code that runs in the
game container with its restrictions (D21); the panel cannot vouch for them (D64–D67).

## Service hardening

The systemd unit runs as a dedicated `mesquita-panel` user with `NoNewPrivileges`,
`ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, no capabilities,
`MemoryDenyWriteExecute`, a `@system-service` syscall filter, restricted address families and
`UMask=0027`. Data lives in `StateDirectory=/var/lib/mesquita-panel` (mode 0750). See
[`INSTALL.md`](INSTALL.md).

## Audit

Administrative actions are written to `audit_log` with user, IP, target and outcome: logins,
setup, password change, server create/start/stop/restart/kill/update/delete, console commands,
file changes and backup actions. The most recent events appear on the dashboard.

## Known gaps

- **No TLS of its own.** Over Tailscale the transport is encrypted, but the cookie is not
  marked `Secure` without HTTPS. Planned: evaluate `tailscale serve` for HTTPS.
- **Single administrator.** There are no roles yet.
- **Same-tick edits.** An in-place write with the same size in the same mtime tick is not
  detected by the editor's version check (D35); atomic panel writes are not affected.

## Reporting

This is a personal project. Report problems privately to the repository owner rather than in a
public issue.
