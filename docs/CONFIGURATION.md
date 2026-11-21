# Configuration

The panel reads environment variables only. Under systemd they come from
`/etc/craftkeeper/config.env` (template: `deployments/config.env.example`).
Invalid values stop startup with an explanatory error.

| Variable | Default | Description |
|---|---|---|
| `LISTEN` | `127.0.0.1:8090` | Comma-separated `host:port` list. Each host must be an explicit IP (an empty host such as `:8090`, which would bind every interface, is rejected). All addresses are bound before serving; if any fails, the process exits. |
| `DATA_DIR` | `/var/lib/craftkeeper` | Absolute path. Holds `database.sqlite` (plus WAL files). Must be writable; checked at startup. Disk metrics report this filesystem. |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | Absolute path to the Docker Engine socket. Unreachable Docker is a warning, not a fatal error. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. `debug` logs every HTTP request. |
| `SECURE_COOKIE` | `false` | `true` or `false`. When `true` the session cookie is always `Secure`; set it when HTTPS is terminated by a proxy on loopback (`tailscale serve`, see `INSTALL.md`). Without it, only requests that reach the panel over TLS get a `Secure` cookie. Forwarding headers are never trusted. |
| `GAME_PORTS` | `25601-25700` | Inclusive range (`first-last`, or one port) of host TCP ports given to game servers, within 1024–65535. Must not include a panel port. A new server gets the first port of the range that no server uses and that can be bound right now, unless one is requested. |

## Recommended production values (mesquita-server)

HTTPS through `tailscale serve` (the panel listens on loopback only, so there is no plain-HTTP
path around TLS):

```env
LISTEN=127.0.0.1:8090
SECURE_COOKIE=true
```

Without HTTPS (plain HTTP over Tailscale, which WireGuard already encrypts; the cookie is not
`Secure`): `LISTEN=127.0.0.1:8090,100.126.54.17:8090`.

Port 8080 is already used on this host. Do not use `0.0.0.0`.

## Fixed values (not configurable)

| Setting | Value |
|---|---|
| Game server data | `<DATA_DIR>/servers/<server id>`, mounted as `/data` in the container; deleted servers kept (when files are not deleted) in `servers/.deleted/<id>-<unix time>` |
| Backups | `<DATA_DIR>/backups/<server id>/<backup id>.zip`, same disk as the servers (download them to keep copies elsewhere); a restore moves the replaced files to `servers/.deleted/<id>-<unix time>-pre-restore` |
| Backup disk reserve | 2 GiB of free space is always left; one archive written at a time; a running game must confirm `save-all flush` within 60 s |
| Backup schedule | every 0 (off)–720 hours per server, keeping 0 (all)–100 scheduled backups; checked every minute |
| Game port bind address | `0.0.0.0` (all interfaces). Internet exposure still depends on the router's port forwarding. |
| Game server images | `itzg/minecraft-server` release 2026.11.0, one per Java version, each pinned by digest and pulled the first time a server needs it: `-java8` (Minecraft ≤ 1.16.5), `-java17` (1.17–1.20.4), `-java21` (1.20.5–1.21.x), Java 25 (26.x and `LATEST`) |
| Server types | Vanilla, Fabric, Forge, NeoForge. Modded servers pin the exact Minecraft release and loader build at creation (the game image never upgrades the loader by itself) |
| Version sources | HTTPS, read by the panel to list versions and cached for 1 hour: `piston-meta.mojang.com` (vanilla list only), `meta.fabricmc.net`, `maven.minecraftforge.net` + `files.minecraftforge.net`, `maven.neoforged.net`. 15 s timeout, 8 MiB per response. The game image downloads the server and loader from the same official hosts on first start, so the host needs outbound HTTPS to them |
| Game container policy | runs as the panel's uid:gid (never root: `1000:1000` when the panel itself runs as root), which owns the server's data directory; no capabilities; `no-new-privileges`; no swap; 2048 PIDs; logs `json-file` 3 × 10 MB; restart `unless-stopped`; graceful stop timeout 60 s; default bridge network |
| Crash loop | 3 automatic restarts (restart policy) within 10 minutes: the panel stops the server and marks it `crashed` until the next start |
| Server memory limit | 512–16384 MB (JVM heap = 75 % of it) |
| Server CPU limit | 0.5 core up to the number of host CPUs |
| Session absolute lifetime | 7 days |
| Session idle timeout | 24 hours |
| Login rate limit | 5 failures per username / 20 per IP, per 15-minute window |
| Password length | 12–256 |
| Argon2id | m=19 MiB, t=2, p=1; at most 2 concurrent hashes |
| JSON request body limit | 64 KiB |
| Metrics cache | 2 seconds |
