#!/usr/bin/env bash
set -euo pipefail
umask 077
root="${1:?Deployment directory is required}"
commit="${2:?Commit is required}"
[[ "$root" == "$HOME/fiberlab" && "$commit" =~ ^[a-f0-9]{40}$ && "$(id -u)" == 1000 ]]
release="$root/releases/$commit"
export DEPLOY_ROOT="$root"
export DOCKER_HOST=unix:///var/run/docker.sock
export DOCKER_CONFIG="$root/shared/docker"
KVM_GID="$(getent group kvm | cut -d: -f3)"
export KVM_GID
export FIBERLAB_IMAGE="fiberlab:$commit"
compose=(docker compose --project-directory "$root" -f "$release/deploy/compose.yml")
previous="$(readlink "$root/current" || true)"
mkdir -p "$root/shared/data" "$root/shared/docker" "$root/backups"
set -a
# shellcheck disable=SC1091
source "$root/shared/.env.next"
set +a
: "${WEB_USERNAME:?Web username is required}" "${WEB_PASSWORD:?Web password is required}" "${CONTROL_KEY:?Control key is required}"
[[ "$WEB_USERNAME" =~ ^[a-zA-Z0-9_-]+$ ]]
"${compose[@]}" config --quiet
docker build --pull --platform linux/amd64 --build-arg "KVM_GID=$KVM_GID" -t "$FIBERLAB_IMAGE" -f "$release/deploy/Dockerfile" "$release"
docker run --rm --user 0:0 --privileged --group-add "$KVM_GID" "$FIBERLAB_IMAGE" doctor --json | jq -e 'all(.[]; (.required | not) or .ok)'

backup_ready=false
rollback() {
    local status=$?
    trap - EXIT
    if (( status != 0 )); then
        "${compose[@]}" stop web app netd || true
        if [[ -n "$previous" ]]; then
            ln -s "$previous" "$root/current.rollback"
            mv -Tf "$root/current.rollback" "$root/current"
            if [[ "$backup_ready" == true ]]; then
                tar -xzf "$root/backups/before-$commit.tar.gz" -C "$root/shared"
            fi
            bash "$root/current/deploy/compose.sh" up -d --force-recreate --wait --wait-timeout 180
        fi
    fi
    exit "$status"
}
trap rollback EXIT
if [[ -n "$previous" ]]; then
    bash "$root/current/deploy/compose.sh" stop web app netd
    tar -czf "$root/backups/before-$commit.tar.gz" -C "$root/shared" data .env htpasswd deployed-sha
    backup_ready=true
fi

mv "$root/shared/.env.next" "$root/shared/.env"
printf "%s:%s\n" "$WEB_USERNAME" "$(printf "%s" "$WEB_PASSWORD" | openssl passwd -6 -stdin)" > "$root/shared/htpasswd"
chmod 644 "$root/shared/htpasswd"
printf "%s" "$CONTROL_KEY" > "$root/shared/data/control.key"
ln -s "releases/$commit" "$root/current.next"
mv -Tf "$root/current.next" "$root/current"
"${compose[@]}" up -d --force-recreate --wait --wait-timeout 180

bash "$release/deploy/provision.sh"
curl --fail --silent --show-error --user "$WEB_USERNAME:$WEB_PASSWORD" -H "Host: sim.karuhundeveloper.com" http://127.0.0.1:18080/api/v1/health
printf "%s\n" "$commit" > "$root/shared/deployed-sha"
printf "Activated Fiberlab release %s\n" "$commit"
