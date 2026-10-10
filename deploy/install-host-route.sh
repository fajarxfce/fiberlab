#!/usr/bin/env bash
set -euo pipefail
if (( EUID != 0 )); then
    echo 'Run this host route installation with sudo.' >&2
    exit 1
fi
source_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
install -o root -g root -m 755 "$source_dir/host-route.sh" /usr/local/sbin/fiberlab-host-route
install -o root -g root -m 644 "$source_dir/fiberlab-route.service" /etc/systemd/system/fiberlab-route.service
systemctl daemon-reload
systemctl enable fiberlab-route.service
systemctl restart fiberlab-route.service
