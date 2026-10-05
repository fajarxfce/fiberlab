# Architecture

Fiberlab separates the local editor, privileged packet runtime, and management
protocol adapters. Go embeds the production Svelte assets; SQLite stores
versioned lab documents and events. No container runtime or external database
is required.

~~~mermaid
flowchart LR
    UI[Svelte editor] -->|HTTP / SSE| APP[Go application + SQLite]
    APP -->|owner-only Unix socket| NET[Go network helper]
    NET --> CHR[QEMU / genuine RouterOS CHR]
    CHR -->|tagged Ethernet| BR[Linux access bridge]
    BR -->|VLAN access port| NS[ONU namespace / pppd]
    CHR -->|RADIUS| R[RADIUS built-in or external]
    IMS[Incident / billing app] -->|RouterOS API / SSH / SNMP| CHR
    IMS -->|SNMP / SSH / Telnet| OLT[OLT reference adapters]
    OLT -->|canonical actions| APP
    NET --> OLT
    APP -->|CWMP over host HTTP/S| ACS[User ACS / GenieACS]
    ACS -->|HTTP Digest connection request| APP
~~~

## Physical model and packet path

An access topology is a directed tree. Router/switch/OLT uplinks are Ethernet;
OLT PON to ODF/splitter/ODP/ONU connections are optical. Validation checks
directions, media, occupied ports, cycles, VLANs, identifiers, serials and MAC
uniqueness, the 128 ONU/PON limit, and the 500 ONU/lab limit.

Each CHR gets six virtio NICs:

| RouterOS interface | Purpose |
| --- | --- |
| ether1 | Management, 10.203.0.10 onward |
| ether2 | Test WAN, 198.18.0.10 onward |
| ether3–ether6 | Canvas lan1–lan4, tagged service VLANs |

The runtime uses a VLAN-filtering bridge per OLT and transparent bridges for
Ethernet switches. Router TAPs and inter-switch veth links transport actual
frames. Each subscriber has a network namespace, a veth access port with
PVID equal to the ONU service VLAN, and a real pppd client. Access ports are
isolated from one another on the same OLT bridge. Passive optical objects
compile into path availability and loss; they do not create a process per
fiber or emulate a GPON optical MAC.

Modelled receive power is OLT transmit power minus fiber loss (0.25 dB/km),
configured connector/additional losses, and splitter insertion loss:
10 log10(ratio) + 0.5 log2(ratio). The receiver sensitivity threshold produces
LOS. Physical OLT/PON ownership is retained while an ONU or OLT is powered off,
allowing a disconnected client to recover without rebuilding the lab.

Fiber cuts, excessive loss, authorization, and ONU power/admin changes update
the relevant veth links. An Ethernet cut disables that trunk; a PON fault
affects only its descendants. An upstream router/switch failure does not remove
the optical transmitter. OLT power removes its management alias; CHR power
controls the QEMU VM with QMP pause/resume. A native RouterOS reboot is available
through its SSH terminal. The ONU reboot action removes its matched PPP session
through the owning CHR's native API, then drops its path for three seconds.
Matching uses username, observed IP and MAC so other subscribers stay connected.

## Runtime and observations

Startup moves through preparing, booting, connecting, and running. Running
means the runtime exists; it does not assert that every subscriber authenticated.
The sampler reads real namespace PPP interfaces, assigned addresses, and kernel
octet counters every three seconds. Actual RADIUS accounting supplies session
IDs and Start/Interim/Stop metadata in built-in mode. External RADIUS owns its
accounting; namespace telemetry remains available.

Each pppd uses a unique interface/link name to avoid collisions in host PID
files, even though the clients live in separate namespaces. Host PPP hooks are
overridden with /bin/true. Terminal commands are an allowlist run inside the
subscriber namespace. The HTTP origin is on the test WAN, so a successful probe
traverses PPPoE, CHR routing/NAT, and the host test bridge.

RADIUS PAP/CHAP and request/response authenticators are validated. Accounting
handles 64-bit counters. Policy updates precede Disconnect-Request so a
reconnect sees the new policy. A failed disconnect of an observed active
subscriber causes the configuration change to fail and roll back.

Optical values are model output. PPP/session/IP/traffic values are observations.
Unknown helper state is surfaced as unknown. Startup errors never become
synthetic connected sessions.

## Ownership and persistence

The application also schedules ONU CWMP sessions, gated on real connected PPP
sessions and the modeled service path. CWMP uses host networking independently
of CHR, with per-session HTTP transports/cookies and at most eight concurrent
sessions. The dedicated connection-request listener exposes only authenticated
ONU Inform triggers. Parameter RPCs persist in SQLite; reboot delegates to the
same scoped ONU power-cycle used by the UI. ACS settings are omitted from helper
payloads, keeping an already-running helper compatible with an app-only upgrade.
There is no extra process or goroutine per idle ACS agent.

The cwmp_state table stores provisioning values independently of editor
revisions and removes them when their lab is deleted. Declarative connection
settings are part of the topology export, while provisioned values stay local
to the lab identity. See [ACS](acs.md) for the supported parameter subset.

Repository builds resolve owner-only `.data` relative to the project containing
the executable; launching `bin/ftthlab` from a file manager uses the same database
as the terminal. Standalone installations use the user's XDG data directory.
An explicit `--data-dir` overrides both. Launching without arguments opens the
browser and reuses an existing instance only when its data directory matches.

The desktop can start the helper through Polkit. The application launches its
own `netd` subcommand with a fixed UID, data directory and socket; HTTP callers
cannot provide commands or arguments. The operating system handles the password
prompt, and concurrent start requests share the same pending launch. The helper
survives a web-app restart; it must be started again after a host reboot.

The root helper uses a private journal
and a locked directory under /var/lib/fiberlab/UID. Root-owned ancestors allow
QEMU to traverse but not modify helper state. Each VM gets its own user-owned
disk/socket directory; privileged logs stay outside it.

The selected image must resolve inside the configured image directory. The
helper copies and verifies its SHA-256 before creating a read-only backing
image in its cache. An overlay filename includes the backing image digest.
QEMU and qemu-img run as the regular application user. SSH host keys persist.

The helper records interfaces, namespaces, PIDs and process start times.
Only recorded resource names are cleaned up. A second helper/cleanup cannot
acquire an active helper's lock. Cleanup failures retain the ownership journal
and are reported; normal restart recovers stale resources. Default host routes,
forwarding sysctls, and firewall rules are not changed. Overlapping management
or test-WAN routes are rejected before construction.

Topology saves use optimistic revisions; conflicting saves return HTTP 409.
Native reference CLI actions use the same app transaction path, so the canvas,
SQLite document, and compiled runtime stay synchronized. Structural edits,
credentials, radius settings, and router service VLAN lists require stopping
the lab. Positions and supported live controls can change while it runs.

JSON snapshots export declarative topology, accounts and faults. They do not
include arbitrary manual RouterOS changes made in a VM console. Those persist
in that lab's local overlay; use native RouterOS export/backup for them.

## Resource bounds

- Maximum 500 ONU clients, 8 CHRs and 8 OLTs per lab.
- Up to 8 CWMP sessions; each has a 90-second deadline, 128 RPC limit and 2 MiB
  SOAP body limit. HTTP connection requests use bounded headers and timeouts.
- One active lab per helper; fixed management/test subnets prevent overlapping runs.
- Up to 32 RADIUS request handlers, 16 CLI connections per OLT and 4 SSH channels
  per connection.
- At most two captures, each 1–30 seconds and approximately 8 MiB.
- Captures attach a kernel filter for only the selected lab interface indices.
  Shared optical paths combine all descendant Ethernet endpoints.
- Process logs rotate at 128 KiB; one previous file is retained per process.
- SQLite retains 2,000 events per lab; SSE and runtime history are bounded.
- UI terminal code loads on demand. Opening a lab or resizing the canvas fits
  the complete graph using its measured bounds; subsequent rendering culls
  off-screen nodes. Production builds omit source maps.
