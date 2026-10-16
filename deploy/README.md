# Production deployment

The existing Cloudflare tunnel sends `sim.karuhundeveloper.com` to `http://localhost:18080`. Releases can be installed over the owner's SSH connection. Optional GitHub Actions deployment runs backend race tests, frontend build and type checks, browser tests, and deployment validation before activating a release through Cloudflare SSH.

The mini PC uses native Docker Engine at `/var/run/docker.sock`, alongside the existing Docker Desktop installation. Native Linux containers are necessary for `/dev/kvm`, TAP devices, PPP, and network namespaces. The deployment scripts select that socket explicitly.

The Compose project has two services: the network runtime and an authenticated nginx gateway on host loopback port 18080. The runtime starts the root network helper, the Go application as UID 1000 with no capabilities, and a TCP proxy as UID 1000. These processes share one container so they keep the same network namespace across Docker restarts. If any process exits, the supervisor stops the others and Docker restarts the runtime together. Stopping the container gives the helper time to clean up the lab.

The runtime's isolated network prevents lab SSH and RADIUS listeners from colliding with host services. The TCP proxy publishes the loopback application at host loopback port 18787. nginx validates browser origins, supports SSE and terminal WebSockets, and blocks the private control endpoint.

The Docker network uses `172.18.0.0/16`, with the runtime fixed at `172.18.0.2`. On this mini PC that range belongs to Fiberlab. When deploying elsewhere, check for a conflicting network before using this configuration.

Install the host management route once after activating the first release:

```sh
sudo bash ~/fiberlab/current/deploy/install-host-route.sh
```

The `fiberlab-route.service` unit restores `10.203.0.0/24 via 172.18.0.2` when Docker starts. The runtime translates incoming management traffic so CHR can return packets to the host. Both the route and translation survive normal container restarts; the unit also restores the route after a host reboot. If the Docker network is manually removed and recreated, restart `fiberlab-route.service`.

With the lab running, the mini PC and an FTTH application running on the host can use `10.203.0.10` for MikroTik (WinBox 8291, API 8728, SSH 22) and `10.203.0.64` for OLT (SSH 22, SNMP 161). Device credentials are shown in **Connections**. These private device IPs are separate from the Cloudflare web domain; a laptop needs its own SSH forwarding or VPN route to reach them.

Internet access is optional. Set `FIBERLAB_INTERNET=1` in `~/fiberlab/shared/.env` (and in `FIBERLAB_ENV` when using automatic deployment). Recreate the runtime with `bash ~/fiberlab/current/deploy/compose.sh up -d --force-recreate runtime`, then start the lab again. The runtime installs NAT from `198.18.0.0/24` through its Docker uplink on every container start. The default is `0`, which leaves the test WAN without Internet NAT.

For each CHR that should use this uplink, add a default route and DNS servers in its RouterOS terminal. Inspect `/ip route print where dst-address=0.0.0.0/0` and `/ip dns print` first; keep any existing configuration that already provides Internet access. A newly provisioned CHR has neither:

```routeros
/ip route add dst-address=0.0.0.0/0 gateway=198.18.0.1 comment=fiberlab-internet
/ip dns set servers=1.1.1.1,8.8.8.8
/ping 1.1.1.1 count=4
/tool fetch url=https://example.com/ output=none
```

These RouterOS settings persist in that router's disk across lab stop/start and runtime recreation. Use the `fiberlab-internet` comment: the exact `fiberlab` comment is reserved for configuration rebuilt by the simulator. New labs or replacement router disks need their own route and DNS configuration. The existing CHR NAT also covers the PPPoE subscriber pool `172.30.0.0/22`; subscribers need a DNS server configured on their client. Changing the deployment flag requires recreating the container; restarting it keeps its existing environment.

External RADIUS respects these existing routes. In particular, selecting a RADIUS server that shares its public IP with an OpenVPN server must keep using the Internet default route. Older releases forced that IP through management with a `/32` route, breaking the VPN. The next lab start removes that old managed route. If RADIUS itself should travel inside the VPN, use its tunnel IP and register the router's tunnel IP as the NAS.

To enable automatic deployment, configure these secrets in GitHub environment `production`, then set the repository variable `DEPLOY_ENABLED` to `true`. Without this variable, pushes run verification only; the installed application continues running.

| Secret | Purpose |
| --- | --- |
| `DEPLOY_SSH_KEY` | Dedicated SSH key with a forced deployment command |
| `DEPLOY_KNOWN_HOSTS` | Pinned SSH server identity |
| `DEPLOY_SSH_PASSWORD` | Owner-provided SSH password; deployment uses the key |
| `FIBERLAB_ENV` | Shell environment containing web login, control key, and bootstrap lab credentials |

`FIBERLAB_ENV` contains `WEB_USERNAME`, `WEB_PASSWORD`, `CONTROL_KEY`, `LAB_ROUTER_PASSWORD`, `LAB_OLT_PASSWORD`, `LAB_SNMP_COMMUNITY`, `LAB_RADIUS_SECRET`, `LAB_PPPOE_PASSWORD`, and `LAB_ACS_PASSWORD`. Use shell-quoted values when necessary. No credential values belong in the repository.

Environment variables `DEPLOY_HOST`, `DEPLOY_USER`, and `APP_URL` identify the target and public route. Initial provisioning downloads the official CHR 7.20.8 image and configures the default lab using those secrets. The `.provisioned` marker prevents later deployments from overwriting lab edits. New labs and credentials edited in the UI remain application data. ACS starts disabled.

To verify real sessions, run the harness inside the helper network namespace so it can inspect CHR directly:

```sh
mkdir -p ~/fiberlab/verification
docker --host unix:///var/run/docker.sock run --rm \
  --network container:fiberlab-runtime-1 --user "$(id -u):$(id -g)" \
  -v "$HOME/fiberlab/current/scripts:/scripts:ro" \
  -v "$HOME/fiberlab/verification:/output" python:3.13-slim \
  python /scripts/verify_runtime.py --url http://127.0.0.1:18787 \
  --onus 8 --seconds 60 --faults --output /output
```

Persistent data lives at `~/fiberlab/shared/data`, with root-managed network runtime at `/var/lib/fiberlab`. Deployment builds before stopping the previous release, backs up stopped application data, and restores the previous release when activation fails. Deploying while a lab is running stops that lab; start it again after deployment.

On the mini PC, use these commands:

```sh
bash ~/fiberlab/current/deploy/compose.sh ps
bash ~/fiberlab/current/deploy/compose.sh logs --tail 100
bash ~/fiberlab/current/deploy/compose.sh restart
```

The services start automatically with Docker after a mini PC reboot. Restarting the runtime stops any running lab; click **Run** to start its devices again. A reboot is not needed after a normal deployment.

To change the web login, update `FIBERLAB_ENV` and run a deployment with a new commit. Runtime environment files are installed with mode 600. The trusted SSH receiver is `~/fiberlab/bin/ssh-deploy.sh`; update that file through the owner SSH session when changing the receiver protocol.
