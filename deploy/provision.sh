#!/usr/bin/env bash
set -euo pipefail
umask 077
: "${DEPLOY_ROOT:?}" "${LAB_ROUTER_PASSWORD:?}" "${LAB_OLT_PASSWORD:?}" "${LAB_SNMP_COMMUNITY:?}"
: "${LAB_RADIUS_SECRET:?}" "${LAB_PPPOE_PASSWORD:?}" "${LAB_ACS_PASSWORD:?}"
marker="$DEPLOY_ROOT/shared/data/.provisioned"
[[ ! -e "$marker" ]] || exit 0
api=http://127.0.0.1:18787/api/v1
version=7.20.8
curl --fail --silent --show-error -H 'Content-Type: application/json' \
    --data "{\"version\":\"$version\"}" "$api/images/fetch" > /dev/null
ready=false
for ((i=0; i<120; i++)); do
    status="$(curl --fail --silent --show-error "$api/images")"
    if jq -e --arg id "chr-$version" 'any(.images[]; .id == $id)' <<< "$status" > /dev/null; then
        ready=true
        break
    fi
    if jq -e '.job.status == "error"' <<< "$status" > /dev/null; then
        echo "Official CHR image download failed." >&2
        exit 1
    fi
    sleep 5
done
[[ "$ready" == true ]]
lab="$(curl --fail --silent --show-error "$api/labs" | jq -er '.[0].id')"
[[ "$lab" =~ ^[a-zA-Z0-9_-]+$ ]]
curl --fail --silent --show-error "$api/labs/$lab" | jq --arg image "chr-$version" '
    .name = "Mini PC · Fiberlab" | .imageId = $image |
    .radius.secret = env.LAB_RADIUS_SECRET |
    .nodes |= map(.config.community = env.LAB_SNMP_COMMUNITY |
        if .kind == "router" then .config.username = "admin" | .config.password = env.LAB_ROUTER_PASSWORD
        elif .kind == "olt" then .config.username = "admin" | .config.password = env.LAB_OLT_PASSWORD | .config.community = env.LAB_SNMP_COMMUNITY
        else . end) |
    .subscribers |= map(.password = env.LAB_PPPOE_PASSWORD) |
    .acs = {enabled: false, url: "http://127.0.0.1:7547", username: "fiberlab", password: env.LAB_ACS_PASSWORD,
        periodicInformSeconds: 60, connectionRequestListen: "127.0.0.1:7548", connectionRequestUrl: "http://127.0.0.1:7548",
        connectionRequestUsername: "fiberlab", connectionRequestPassword: env.LAB_ACS_PASSWORD}
' | curl --fail --silent --show-error -X PUT -H 'Content-Type: application/json' --data-binary @- "$api/labs/$lab" > /dev/null
printf "%s\n" "$lab" > "$marker"
echo "Initial lab provisioned from production environment secrets."
