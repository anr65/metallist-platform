#!/usr/bin/env bash
set -euo pipefail

sha="${1:?pass the exact verified main commit SHA}"
if [[ ! "$sha" =~ ^[0-9a-f]{40}$ ]]; then
  echo 'invalid SHA' >&2
  exit 2
fi

repo=/opt/metallist-platform/source
release=/opt/metallist-platform/releases/$sha
current=/opt/metallist-platform/current

git -C "$repo" fetch origin main
if [[ "$(git -C "$repo" rev-parse FETCH_HEAD)" != "$sha" ]]; then
  echo 'SHA is not the current origin/main tip' >&2
  exit 2
fi

mkdir -p "$release"
git -C "$repo" archive "$sha" | tar -x -C "$release"
(cd "$release" && go build -trimpath -o platform ./cmd/platform)

previous="$(readlink -f "$current")"
ln -sfn "$release" "$current.next"
mv -Tf "$current.next" "$current"
if ! sudo systemctl restart metallist-platform.service || ! sudo systemctl is-active --quiet metallist-platform.service; then
  ln -sfn "$previous" "$current.next"
  mv -Tf "$current.next" "$current"
  sudo systemctl restart metallist-platform.service
  echo 'deployment failed; previous release restored' >&2
  exit 1
fi

for attempt in {1..30}; do
  if curl -fsS --max-time 2 http://127.0.0.1:8080/health >/dev/null 2>&1; then
    echo "deployed $sha"
    exit 0
  fi
  sleep 1
done

ln -sfn "$previous" "$current.next"
mv -Tf "$current.next" "$current"
sudo systemctl restart metallist-platform.service
echo 'health check failed; previous release restored' >&2
exit 1
