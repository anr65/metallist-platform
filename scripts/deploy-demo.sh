#!/usr/bin/env bash
set -euo pipefail
sha="${1:?pass exact verified Git commit SHA}"
if [[ ! "$sha" =~ ^[0-9a-f]{40}$ ]]; then echo 'invalid SHA' >&2; exit 2; fi
repo=/opt/metallist-platform/source
release=/opt/metallist-platform/releases/$sha
git -C "$repo" fetch origin demo-v1
git -C "$repo" cat-file -e "$sha^{commit}"
git -C "$repo" merge-base --is-ancestor "$sha" FETCH_HEAD
mkdir -p "$release"
git -C "$repo" archive "$sha" | tar -x -C "$release"
(cd "$release" && go build -trimpath -o platform ./cmd/platform)
ln -sfn "$release" /opt/metallist-platform/current.next
mv -Tf /opt/metallist-platform/current.next /opt/metallist-platform/current
sudo systemctl restart metallist-demo.service
sudo systemctl is-active --quiet metallist-demo.service
ready=0
for attempt in {1..30}; do
  if curl -fsS --max-time 2 http://127.0.0.1:8080/health >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [[ "$ready" != 1 ]]; then
  echo 'demo service did not become ready within 30 seconds' >&2
  exit 1
fi
echo "deployed $sha"
