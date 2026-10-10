#!/usr/bin/env bash
set -euo pipefail
umask 077

# Keep all network-dependent processes in one container. Independently
# restarting containers with network_mode: service:netd can retain a stale
# namespace even though their individual health checks continue to pass.
children=()
# shellcheck disable=SC2329 # Invoked by the EXIT trap, including startup failures.
cleanup() {
    trap '' TERM INT
    for child in "${children[@]}"; do
        kill -TERM "$child" 2>/dev/null || true
    done
    for child in "${children[@]}"; do
        wait "$child" 2>/dev/null || true
    done
}
trap 'cleanup' EXIT
trap 'exit 0' TERM INT

# The host routes management traffic through this container. CHR has no route
# back to the Docker subnet, so use the lab gateway as the source for replies.
management_nat=(POSTROUTING '!' -s 10.203.0.0/24 -d 10.203.0.0/24 -o flab-mgmt -j MASQUERADE)
if ! iptables -t nat -C "${management_nat[@]}" 2>/dev/null; then
    iptables -t nat -A "${management_nat[@]}"
fi

wait_ready() {
    local child="$1"
    shift
    for ((attempt=0; attempt<60; attempt++)); do
        if ! kill -0 "$child" 2>/dev/null; then
            echo "Fiberlab process exited during startup." >&2
            return 1
        fi
        if curl --fail --silent --max-time 2 "$@" > /dev/null; then
            return 0
        fi
        sleep 0.5
    done
    echo "Fiberlab process did not become ready." >&2
    return 1
}

/usr/local/bin/ftthlab netd --data-dir /data --uid 1000 --runtime-dir /var/lib/fiberlab/1000 &
children+=("$!")
wait_ready "${children[0]}" --unix-socket /data/netd.sock http://localhost/health

# Only netd needs privileges. Drop the application's UID, capabilities, and
# ability to gain privileges, including for any commands it launches.
unprivileged=(setpriv --reuid=1000 --regid=1000 --init-groups
    --inh-caps=-all --ambient-caps=-all --bounding-set=-all --no-new-privs)
"${unprivileged[@]}" /usr/local/bin/ftthlab serve --data-dir /data --listen 127.0.0.1:18787 &
children+=("$!")
wait_ready "${children[1]}" http://127.0.0.1:18787/api/v1/health

"${unprivileged[@]}" /usr/bin/socat TCP-LISTEN:18788,reuseaddr,fork TCP:127.0.0.1:18787 &
children+=("$!")

# Losing any process shuts down the others and lets Docker restart the whole
# runtime in one namespace. netd receives SIGTERM for normal lab cleanup.
status=0
wait -n "${children[@]}" || status=$?
if ((status == 0)); then
    status=1
fi
exit "$status"
