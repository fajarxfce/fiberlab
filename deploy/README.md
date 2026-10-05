# Production deployment

`main` runs `.github/workflows/deploy.yml`: backend race tests, frontend build and type checks, browser tests, deployment validation, then SSH deployment through Cloudflare Access. The existing tunnel sends `sim.karuhundeveloper.com` to `http://localhost:18080`.

The mini PC uses native Docker Engine at `/var/run/docker.sock`, alongside the existing Docker Desktop installation. Native Linux containers are necessary for `/dev/kvm`, TAP devices, PPP, and network namespaces. The deployment scripts select that socket explicitly.

The Compose project has three services: an unprivileged Go application on loopback port 18787, a privileged network helper, and an authenticated nginx gateway on loopback port 18080. nginx validates browser origins, supports SSE and terminal WebSockets, and blocks the private control endpoint.

GitHub environment `production` owns all deployment credentials:

| Secret | Purpose |
| --- | --- |
| `DEPLOY_SSH_KEY` | Dedicated SSH key with a forced deployment command |
| `DEPLOY_KNOWN_HOSTS` | Pinned SSH server identity |
| `DEPLOY_SSH_PASSWORD` | Owner-provided SSH password; deployment uses the key |
| `FIBERLAB_ENV` | Shell environment containing web login, control key, and bootstrap lab credentials |

`FIBERLAB_ENV` contains `WEB_USERNAME`, `WEB_PASSWORD`, `CONTROL_KEY`, `LAB_ROUTER_PASSWORD`, `LAB_OLT_PASSWORD`, `LAB_SNMP_COMMUNITY`, `LAB_RADIUS_SECRET`, `LAB_PPPOE_PASSWORD`, and `LAB_ACS_PASSWORD`. Use shell-quoted values when necessary. No credential values belong in the repository.

Environment variables `DEPLOY_HOST`, `DEPLOY_USER`, and `APP_URL` identify the target and public route. Initial provisioning downloads the official CHR 7.20.8 image and configures the default lab using those secrets. The `.provisioned` marker prevents later deployments from overwriting lab edits. New labs and credentials edited in the UI remain application data. ACS starts disabled.

Persistent data lives at `~/fiberlab/shared/data`, with root-managed network runtime at `/var/lib/fiberlab`. Deployment builds before stopping the previous release, backs up stopped application data, and restores the previous release when activation fails. Deploying while a lab is running stops that lab; start it again after deployment.

On the mini PC, use these commands:

```sh
bash ~/fiberlab/current/deploy/compose.sh ps
bash ~/fiberlab/current/deploy/compose.sh logs --tail 100
bash ~/fiberlab/current/deploy/compose.sh restart
```

To change the web login, update `FIBERLAB_ENV` and run a deployment with a new commit. Runtime environment files are installed with mode 600. The trusted SSH receiver is `~/fiberlab/bin/ssh-deploy.sh`; update that file through the owner SSH session when changing the receiver protocol.
