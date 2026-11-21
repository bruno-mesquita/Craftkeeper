#!/usr/bin/env bash
# Upgrades an installed Craftkeeper on this host: build and test, back the database up, swap
# the binary, restart, check health, and roll back binary and database if the new version
# does not come up. First installation is manual: see docs/INSTALL.md.
#
# Usage (from the repository root, in a real terminal): sudo make deploy
#   SKIP_BUILD=1  (sudo make deploy SKIP_BUILD=1) install the binary already in bin/ (built and tested by `sudo make check`)
#   HEALTH_URL    health endpoint (default http://127.0.0.1:8090/api/v1/health)
set -euo pipefail

SERVICE=craftkeeper
BIN=/opt/craftkeeper/craftkeeper
DATA=/var/lib/craftkeeper
BACKUPS=/var/backups/craftkeeper
KEEP=5
HEALTH_URL=${HEALTH_URL:-http://127.0.0.1:8090/api/v1/health}
HEALTH_SECONDS=30

log() { printf '[deploy] %s\n' "$*"; }
die() { printf '[deploy] error: %s\n' "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "run as root: sudo make deploy"
[[ -f go.mod && -f deployments/deploy.sh ]] || die "run from the repository root"
systemctl cat "$SERVICE.service" >/dev/null 2>&1 ||
	die "$SERVICE.service is not installed; follow docs/INSTALL.md for the first installation"
[[ -x $BIN ]] || die "$BIN is missing; follow docs/INSTALL.md for the first installation"

# git refuses a repository owned by another user unless told otherwise; ask as that user.
as_owner() { if [[ -n ${SUDO_USER:-} ]]; then sudo -u "$SUDO_USER" "$@"; else "$@"; fi; }
rev=$(as_owner git rev-parse --short HEAD)
if [[ -n $(as_owner git status --porcelain) ]]; then
	log "warning: the working tree has uncommitted changes; deploying them as $rev+dirty"
	rev+=-dirty
fi

if [[ ${SKIP_BUILD:-} != 1 ]]; then
	log "building and testing $rev (make check)"
	make check || die "make check failed; nothing was changed (see .cache/last-run.log)"
fi
[[ -x bin/craftkeeper ]] || die "bin/craftkeeper is missing"

backup="$BACKUPS/$(date -u +%Y%m%dT%H%M%SZ)-$rev"
log "stopping $SERVICE (game servers keep running)"
systemctl stop "$SERVICE"

# Cold copy: with the service stopped the database and its WAL are consistent.
install -d -m 0700 "$backup"
for f in database.sqlite database.sqlite-wal database.sqlite-shm; do
	if [[ -e $DATA/$f ]]; then cp -p "$DATA/$f" "$backup/"; fi
done
cp -p "$BIN" "$backup/craftkeeper"
log "backed up database and binary to $backup"

rollback() {
	log "rolling back to the previous binary and database"
	systemctl stop "$SERVICE" || true
	install -m 0755 "$backup/craftkeeper" "$BIN"
	rm -f "$DATA/database.sqlite-wal" "$DATA/database.sqlite-shm"
	for f in database.sqlite database.sqlite-wal database.sqlite-shm; do
		if [[ -e $backup/$f ]]; then cp -p "$backup/$f" "$DATA/"; fi
	done
	systemctl start "$SERVICE" || true
	journalctl -u "$SERVICE" -n 50 --no-pager || true
	die "deploy of $rev failed; the previous version is running again"
}

install -m 0755 bin/craftkeeper "$BIN"
systemctl start "$SERVICE" || rollback

log "waiting for $HEALTH_URL"
for ((i = 0; i < HEALTH_SECONDS; i++)); do
	if curl -fsS --max-time 2 "$HEALTH_URL" 2>/dev/null | grep -q '"ok"'; then
		# Keep the newest backups only.
		find "$BACKUPS" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort -r | tail -n +$((KEEP + 1)) |
			while read -r old; do rm -rf -- "${BACKUPS:?}/$old"; done
		log "deployed $rev; backup in $backup"
		exit 0
	fi
	sleep 1
done
rollback
