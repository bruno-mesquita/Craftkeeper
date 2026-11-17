# Configuration

The panel reads environment variables only. Under systemd they come from
`/etc/mesquita-panel/config.env` (template: `deployments/config.env.example`).
Invalid values stop startup with an explanatory error.

| Variable | Default | Description |
|---|---|---|
| `MESQUITA_LISTEN` | `127.0.0.1:8090` | Comma-separated `host:port` list. Each host must be an explicit IP (an empty host such as `:8090`, which would bind every interface, is rejected). All addresses are bound before serving; if any fails, the process exits. |
| `MESQUITA_DATA_DIR` | `/var/lib/mesquita-panel` | Absolute path. Holds `database.sqlite` (plus WAL files). Must be writable; checked at startup. Disk metrics report this filesystem. |
| `MESQUITA_DOCKER_SOCKET` | `/var/run/docker.sock` | Absolute path to the Docker Engine socket. Unreachable Docker is a warning, not a fatal error. |
| `MESQUITA_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. `debug` logs every HTTP request. |
| `MESQUITA_GAME_PORTS` | `25601-25700` | Inclusive range (`first-last`, or one port) of host TCP ports given to game servers, within 1024–65535. Must not include a panel port. A new server gets the first port of the range that no server uses and that can be bound right now, unless one is requested. |

## Recommended production values (mesquita-server)

```env
MESQUITA_LISTEN=127.0.0.1:8090,100.126.54.17:8090
```

Port 8080 is already used on this host. Do not use `0.0.0.0`.

## Fixed values (not configurable)

| Setting | Value |
|---|---|
| Game server data | `<MESQUITA_DATA_DIR>/servers/<server id>`, mounted as `/data` in the container; deleted servers kept (when files are not deleted) in `servers/.deleted/<id>-<unix time>` |
| Game port bind address | `0.0.0.0` (all interfaces). Internet exposure still depends on the router's port forwarding. |
| Game server image | `itzg/minecraft-server:2026.11.0` (Java 25), pinned by digest |
| Game container policy | runs as the panel's uid:gid (never root: `1000:1000` when the panel itself runs as root), which owns the server's data directory; no capabilities; `no-new-privileges`; no swap; 2048 PIDs; logs `json-file` 3 × 10 MB; restart `unless-stopped`; graceful stop timeout 60 s; default bridge network |
| Server memory limit | 512–16384 MB (JVM heap = 75 % of it) |
| Server CPU limit | 0.5 core up to the number of host CPUs |
| Session absolute lifetime | 7 days |
| Session idle timeout | 24 hours |
| Login rate limit | 5 failures per username / 20 per IP, per 15-minute window |
| Password length | 12–256 |
| Argon2id | m=19 MiB, t=2, p=1; at most 2 concurrent hashes |
| JSON request body limit | 64 KiB |
| Metrics cache | 2 seconds |
