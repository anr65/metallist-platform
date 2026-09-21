#!/usr/bin/env bash
set -euo pipefail

if [[ $(id -u) != 0 ]]; then
  echo 'production backup must run as root' >&2
  exit 1
fi

umask 077
backup_root=/var/backups/metallist-platform-protected
install -d -m 0700 -o root -g root "$backup_root"
stage=$(mktemp -d "$backup_root/.backup-XXXXXXXX")
trap 'rm -rf -- "$stage"' EXIT

sudo -u postgres pg_dump -Fc -d metallist_demo > "$stage/database.dump"
pg_restore -l "$stage/database.dump" >/dev/null
tar -C /var/lib/metallist-platform -czf "$stage/files.tar.gz" documents pan
tar -tzf "$stage/files.tar.gz" >/dev/null
cp /etc/metallist-platform/pan.key "$stage/pan.key"
cp /etc/metallist-platform/production.env "$stage/production.env"
(cd "$stage" && sha256sum database.dump files.tar.gz pan.key production.env > SHA256SUMS)

destination="$backup_root/$(date -u +%Y%m%dT%H%M%SZ)"
mv -T "$stage" "$destination"
trap - EXIT
echo "production backup created: $destination"
