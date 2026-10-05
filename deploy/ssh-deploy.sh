#!/usr/bin/env bash
set -euo pipefail
umask 077
root="$HOME/fiberlab"
read -r verb commit checksum extra <<< "${SSH_ORIGINAL_COMMAND:-}"
if [[ "$verb" != deploy || ! "$commit" =~ ^[a-f0-9]{40}$ || ! "$checksum" =~ ^[a-f0-9]{64}$ || -n "$extra" ]]; then
    echo "Only a verified Fiberlab release deployment is allowed." >&2
    exit 1
fi
mkdir -p "$root/uploads" "$root/releases"
exec 9> "$root/deploy.lock"
flock -w 300 9
archive="$(mktemp "$root/uploads/release.XXXXXX.tar.gz")"
trap 'rm -f "$archive"' EXIT
cat > "$archive"
printf "%s  %s\n" "$checksum" "$archive" | sha256sum --check --status
if tar -tzf "$archive" | grep -Eq '(^/|(^|/)\.\.(/|$))'; then
    echo "Invalid archive path." >&2
    exit 1
fi
if [[ "$(cat "$root/shared/deployed-sha" 2>/dev/null || true)" == "$commit" ]]; then
    echo "This commit is already active."
    exit 0
fi
release="$root/releases/$commit"
if [[ -e "$release" ]]; then
    mv "$release" "$root/uploads/failed-$commit-$(date +%s)"
fi
mkdir -m 755 "$release"
tar --extract --gzip --file "$archive" --directory "$release" --no-same-owner --no-same-permissions
test -s "$release/production.env"
install -m 600 "$release/production.env" "$root/shared/.env.next"
rm "$release/production.env"
bash "$release/deploy/activate.sh" "$root" "$commit"
