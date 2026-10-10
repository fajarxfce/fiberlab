#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == --remove ]]; then
    ip route del 10.203.0.0/24 via 172.18.0.2 proto static 2>/dev/null || true
    exit 0
fi

# The Compose address is fixed, so container replacement does not invalidate
# this route. Wait for Docker's automatic container startup during host boot.
for ((attempt=0; attempt<60; attempt++)); do
    state="$(docker --host unix:///var/run/docker.sock inspect --format \
        '{{.State.Running}} {{with index .NetworkSettings.Networks "fiberlab_default"}}{{.IPAddress}}{{end}}' \
        fiberlab-runtime-1 2>/dev/null || true)"
    if [[ "$state" == 'true 172.18.0.2' ]]; then
        ip route replace 10.203.0.0/24 via 172.18.0.2 proto static
        exit 0
    fi
    sleep 1
done

echo 'Fiberlab runtime did not start at its configured Docker address.' >&2
exit 1
