# Installation (Ubuntu, systemd)

Production does **not** need Go, Node or any build tool: only the static binary.
Every step runs with `sudo` and must not touch Crafty, its containers, networks or ports.

## 0. Build (development side)

```bash
make build          # -> bin/craftkeeper (linux/amd64, static, CGO disabled)
```

## 1. Check the architecture

```bash
uname -m            # must print x86_64
```

## 2. Check Docker (read-only)

```bash
sudo docker version --format '{{.Server.Version}}'
```

Without Docker access the panel still starts: the dashboard shows "Docker: unreachable"
and server management is unavailable.

**Server management needs Docker access.** The service user must be in the `docker` group,
which is root-equivalent: anyone controlling the panel process controls the host. The panel
only acts on containers labelled `craftkeeper.managed=true` with a server ID it knows, but only
grant this deliberately:

```bash
sudo usermod -aG docker craftkeeper
# and uncomment SupplementaryGroups=docker in the unit (step 7)
```

After starting, check in the log that `docker is reachable` appears. The unit uses
`ProtectSystem=strict`; connecting to `/var/run/docker.sock` is not a write to the
filesystem, so it is expected to work — confirm on first deploy.

## 3. Check the ports

```bash
sudo ss -ltnp | grep -E ':8090\b' || echo "8090 free"
# Game servers use GAME_PORTS (default 25601-25700); list what is already bound there:
sudo ss -ltn | awk '{print $4}' | grep -E ':256(0[1-9]|[1-9][0-9])$|:25700$' || echo "game range free"
```

If 8090 is taken, choose another free port and use it in step 6.

## 4. Create the service user and directories

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin craftkeeper
sudo install -d -m 0755 /opt/craftkeeper
sudo install -d -m 0750 -o root -g craftkeeper /etc/craftkeeper
# /var/lib/craftkeeper is created by systemd (StateDirectory=).
```

## 5. Install the binary

```bash
sudo install -m 0755 bin/craftkeeper /opt/craftkeeper/craftkeeper
```

(From another machine: `scp bin/craftkeeper mesquita-server:/tmp/` first.)

## 6. Configure

```bash
sudo install -m 0640 -o root -g craftkeeper deployments/config.env.example /etc/craftkeeper/config.env
sudoedit /etc/craftkeeper/config.env
```

See `CONFIGURATION.md`. Keep the panel on `127.0.0.1` and the Tailscale IP only.

## 7. Install the systemd unit

```bash
sudo install -m 0644 deployments/systemd/craftkeeper.service /etc/systemd/system/
sudo systemctl daemon-reload
```

## 8. Start

```bash
sudo systemctl enable --now craftkeeper
systemctl status craftkeeper
curl -s http://127.0.0.1:8090/api/v1/health     # {"status":"ok"}
```

## 8b. HTTPS with `tailscale serve`

The panel does not terminate TLS itself: Tailscale serves it over HTTPS with a certificate it
obtains and renews for the machine's tailnet name. Requires MagicDNS and HTTPS certificates to be
enabled in the Tailscale admin console (DNS page).

```bash
# config.env: LISTEN=127.0.0.1:8090 and SECURE_COOKIE=true, then restart
sudo tailscale serve --bg --https=443 http://127.0.0.1:8090
tailscale serve status       # shows https://<machine>.<tailnet>.ts.net
```

`tailscale serve` only listens on the Tailscale interface, so it does not clash with other
services bound to port 443 on the LAN; if it does, pick another `--https=` port. The setting
persists across reboots (`--bg`); `sudo tailscale serve --https=443 off` removes it.

Check after enabling it: login works and the cookie is `Secure` (browser dev tools), and the
console connects (the WebSocket requires `Origin` to match `Host`, so the proxy must keep the
`Host` header). Behind the proxy every client appears as `127.0.0.1` in the audit log and the
per-IP login limit.

## 9. Logs

```bash
journalctl -u craftkeeper -f
```

## 10. Initial administrator setup

While no administrator exists, every start logs a **one-time setup token**:

```bash
journalctl -u craftkeeper | grep setup_token
```

Open `https://<machine>.<tailnet>.ts.net` (with `tailscale serve`), `http://100.126.54.17:8090`
(plain HTTP over Tailscale, if the panel listens there) or tunnel with
`ssh -L 8090:127.0.0.1:8090 mesquita-server`, enter the token, and create the administrator.
After that the setup endpoint is permanently closed. There is no default password.

## Upgrade

From the repository on the server, in a real terminal:

```bash
sudo make deploy            # output also in .cache/deploy.log
```

`deployments/deploy.sh` only upgrades an existing installation (steps 1–8 above) and stops
before changing anything if the service or binary is missing or `make check` fails. Then it:

1. stops the service (game server containers keep running);
2. copies `database.sqlite` (+ WAL files) and the current binary to
   `/var/backups/craftkeeper/<UTC time>-<git rev>/` (mode 0700; the newest 5 are kept);
3. installs `bin/craftkeeper`, starts the service and polls `/api/v1/health` for 30 s;
4. if the service does not come up, restores **both** the previous binary and the database
   (migrations only move forward, so the old binary needs the old schema), starts it again and
   prints the last 50 journal lines.

`sudo make deploy SKIP_BUILD=1` installs the binary already in `bin/` (after `sudo make check`);
`HEALTH_URL=...` (same way) overrides the health endpoint when the panel does not listen on `127.0.0.1:8090`.

To go back to an older backup by hand:

```bash
sudo systemctl stop craftkeeper
sudo install -m 0755 /var/backups/craftkeeper/<dir>/craftkeeper /opt/craftkeeper/craftkeeper
sudo rm -f /var/lib/craftkeeper/database.sqlite-wal /var/lib/craftkeeper/database.sqlite-shm
sudo cp -p /var/backups/craftkeeper/<dir>/database.sqlite* /var/lib/craftkeeper/
sudo systemctl start craftkeeper
```

## Uninstall

```bash
sudo systemctl disable --now craftkeeper
sudo rm /etc/systemd/system/craftkeeper.service && sudo systemctl daemon-reload
sudo rm -r /opt/craftkeeper /etc/craftkeeper   # /var/lib/craftkeeper holds data: remove deliberately
# Game server containers keep running (restart policy unless-stopped). List them before deciding:
sudo docker ps -a --filter label=craftkeeper.managed=true
```

## Verify Crafty is untouched

```bash
sudo docker ps --format '{{.Names}}\t{{.Status}}'
```
