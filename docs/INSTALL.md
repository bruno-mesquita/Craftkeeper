# Installation (Ubuntu, systemd)

Production does **not** need Go, Node or any build tool: only the static binary.
Every step runs with `sudo` and must not touch Crafty, its containers, networks or ports.

## 0. Build (development side)

```bash
make build          # -> bin/mesquita-panel (linux/amd64, static, CGO disabled)
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
only acts on containers labelled `mesquita.managed=true` with a server ID it knows, but only
grant this deliberately:

```bash
sudo usermod -aG docker mesquita-panel
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
sudo useradd --system --no-create-home --shell /usr/sbin/nologin mesquita-panel
sudo install -d -m 0755 /opt/mesquita-panel
sudo install -d -m 0750 -o root -g mesquita-panel /etc/mesquita-panel
# /var/lib/mesquita-panel is created by systemd (StateDirectory=).
```

## 5. Install the binary

```bash
sudo install -m 0755 bin/mesquita-panel /opt/mesquita-panel/mesquita-panel
```

(From another machine: `scp bin/mesquita-panel mesquita-server:/tmp/` first.)

## 6. Configure

```bash
sudo install -m 0640 -o root -g mesquita-panel deployments/config.env.example /etc/mesquita-panel/config.env
sudoedit /etc/mesquita-panel/config.env
```

See `CONFIGURATION.md`. Keep the panel on `127.0.0.1` and the Tailscale IP only.

## 7. Install the systemd unit

```bash
sudo install -m 0644 deployments/systemd/mesquita-panel.service /etc/systemd/system/
sudo systemctl daemon-reload
```

## 8. Start

```bash
sudo systemctl enable --now mesquita-panel
systemctl status mesquita-panel
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
journalctl -u mesquita-panel -f
```

## 10. Initial administrator setup

While no administrator exists, every start logs a **one-time setup token**:

```bash
journalctl -u mesquita-panel | grep setup_token
```

Open `https://<machine>.<tailnet>.ts.net` (with `tailscale serve`), `http://100.126.54.17:8090`
(plain HTTP over Tailscale, if the panel listens there) or tunnel with
`ssh -L 8090:127.0.0.1:8090 mesquita-server`, enter the token, and create the administrator.
After that the setup endpoint is permanently closed. There is no default password.

## Upgrade

```bash
sudo install -m 0755 bin/mesquita-panel /opt/mesquita-panel/mesquita-panel
sudo systemctl restart mesquita-panel
```

Migrations run automatically at startup. Back up first:
`sudo sqlite3 /var/lib/mesquita-panel/database.sqlite ".backup /root/panel-$(date +%F).sqlite"`
(or stop the service and copy the directory).

## Uninstall

```bash
sudo systemctl disable --now mesquita-panel
sudo rm /etc/systemd/system/mesquita-panel.service && sudo systemctl daemon-reload
sudo rm -r /opt/mesquita-panel /etc/mesquita-panel   # /var/lib/mesquita-panel holds data: remove deliberately
# Game server containers keep running (restart policy unless-stopped). List them before deciding:
sudo docker ps -a --filter label=mesquita.managed=true
```

## Verify Crafty is untouched

```bash
sudo docker ps --format '{{.Names}}\t{{.Status}}'
```
