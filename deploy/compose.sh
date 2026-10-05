#!/usr/bin/env bash
set -euo pipefail
export DEPLOY_ROOT="${DEPLOY_ROOT:-$HOME/fiberlab}"
export DOCKER_HOST=unix:///var/run/docker.sock
export DOCKER_CONFIG="$DEPLOY_ROOT/shared/docker"
FIBERLAB_IMAGE="fiberlab:$(cat "$DEPLOY_ROOT/shared/deployed-sha")"
KVM_GID="$(getent group kvm | cut -d: -f3)"
export FIBERLAB_IMAGE KVM_GID
exec docker compose --project-directory "$DEPLOY_ROOT" -f "$DEPLOY_ROOT/current/deploy/compose.yml" "$@"
