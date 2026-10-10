# Integration contracts

The standard management services use each device's management IP and native
ports, shown in Integration. The HTTP API below controls the lab. It is not a
replacement for native device protocols.

## Capability matrix

| Surface | Implementation | Limits |
| --- | --- | --- |
| CHR Winbox TCP 8291 / API TCP 8728 / SSH TCP 22 / SNMP UDP 161 | Genuine RouterOS 7.20.8 | CHR license applies; six provisioned NICs |
| OLT SNMPv2c UDP 161 | system, IF-MIB/IF-X-MIB subset, FTTHLAB-MIB, HSGQ GPON/EPON ONU telemetry | Read only; no SNMPv3, vendor provisioning or full firmware emulation |
| OLT SSH TCP 22 / Telnet TCP 23 | Password auth, bounded interactive/exec CLI | Explicit lab commands; no vendor firmware grammar |
| ONU telemetry | Modelled optical state + actual PPP observations | No OMCI, ONU firmware or independent management SNMP |
| ONU TR-069 / CWMP | HTTP/SOAP CWMP 1.0, TR-098 subset, Inform, parameter RPCs, Reboot/FactoryReset | Host management transport; simulated Wi-Fi; no vendor firmware or TR-181 |
| RADIUS UDP 1812 / 1813 | PAP, CHAP, Start/Interim/Stop, Message-Authenticator | No EAP, MSCHAP, SQL backend or built-in billing ledger |
| Dynamic authorization | Native Disconnect-Request / ACK on UDP 3799 | Rate changes force reauthentication; no CoA attribute updates |
| Ethernet PCAP | Actual AF_PACKET capture | Shared optical links merge descendants, not GPON physical frames |

The machine-readable matrix is GET /api/v1/capabilities. The [HSGQ profile](hsgq.md)
exposes a G01ID GPON inventory/optical subset plus an E04I EPON compatibility
table for the FTTH HSGQ adapter. Other vendor OIDs return
noSuchObject/endOfMibView; unknown CLI commands return UNSUPPORTED and SSH exec
exit status 1. This is not a claim of complete hardware/firmware equivalence.

## OLT SNMP profile

All OIDs are numeric in these examples, so an external MIB installation is not
required. Use the community shown by the Integration panel (default lab-read).

~~~sh
snmpget -v2c -c lab-read 10.203.0.64 .1.3.6.1.2.1.1.5.0
snmpwalk -v2c -c lab-read 10.203.0.64 .1.3.6.1.2.1.2.2.1
snmpbulkwalk -v2c -c lab-read 10.203.0.64 .1.3.6.1.4.1.32473.42.1.1
~~~

GET, GETNEXT and GETBULK are supported. GETBULK is capped at 32 repetitions and
256 variables, truncated at whole repetition rows for SNMP4J TableUtils.
SET returns notWritable. Responses use numerical OID ordering.
OLT power/admin down stops responses. sysObjectID identifies the experimental
lab profile. The separately documented HSGQ ONU columns use the observed vendor
namespace `.1.3.6.1.4.1.50224`; [HSGQ integration](hsgq.md) explains the two
identity formats, zero/one-based indices, optical values and supported faults.

Standard scalar system OIDs .1.3.6.1.2.1.1.1–7.0 are available. ifNumber is 9:
ifIndex 1 is the uplink; ifIndex 2–9 are pon1–pon8. ifTable exposes ifIndex,
ifDescr, ifType, ifMtu, ifSpeed, ifPhysAddress, ifAdminStatus, ifOperStatus,
ifLastChange, ifInOctets and ifOutOctets. ifXTable exposes ifName,
ifHCInOctets and ifHCOutOctets.

These are logical interface views: octets aggregate observed subscriber PPP
octets, omit Ethernet framing/management traffic, and reset when PPP interfaces
are recreated. ifLastChange is currently zero; discontinuity tracking and
packet/error/drop counters are not implemented. ifSpeed is a descriptive
1 Gbps reference value, not a throughput guarantee.

The experimental base **.1.3.6.1.4.1.32473.42** uses the documentation/example
PEN from RFC 5612; it is not a production-assigned vendor namespace.
ONU rows are sorted by ONU ID within an OLT and indexed from 1.

| OID suffix | Type | Meaning |
| --- | --- | --- |
| .1.1.1.ROW | OCTET STRING | ONU ID |
| .1.1.2.ROW | OCTET STRING | GPON serial |
| .1.1.3.ROW | INTEGER | online=1, los=2, unregistered=3, offline=4 |
| .1.1.4.ROW | INTEGER | Modelled RX in 0.01 dBm; -9999 means unavailable |
| .1.1.5.ROW | INTEGER | PON 1–8 |
| .1.1.6.ROW | INTEGER | Service VLAN |
| .1.1.7.ROW | Counter64 | Subscriber RX bytes from actual PPP |
| .1.1.8.ROW | Counter64 | Subscriber TX bytes from actual PPP |
| .1.1.9.ROW | OCTET STRING | Observed assigned IPv4, empty until connected |
| .1.1.10.ROW | OCTET STRING | PPPoE username |

RX/TX in the ONU table are from the subscriber's perspective. Standard port
counters are from the logical OLT port's perspective.

Optional SNMPv2 traps are sent from the owning OLT management IP to the configured
trap receiver. Notification .0.1 contains sysUpTime, snmpTrapOID, ONU ID (.2.1.0)
and state text (.2.2.0). This is a **lab state-change notification**, not a
vendor dying-gasp or LOS trap fixture. OLT power failure cannot send a trap.
The MIB definition is [FTTHLAB-MIB](FTTHLAB-MIB.txt).

## OLT CLI

~~~sh
ssh admin@10.203.0.64
telnet 10.203.0.64 23
~~~

~~~text
help
lab system
lab capabilities
lab onu list
lab onu onu-0001 authorize
lab onu onu-0001 deauthorize
lab onu onu-0001 enable
lab onu onu-0001 disable
lab onu onu-0001 vlan 100
lab onu onu-0001 reboot
exit
~~~

Configuration actions persist through the app's canonical lab document. The
app must be running for these callbacks. The ONU must belong to the selected
OLT. SSH also accepts a single command through an exec channel. Telnet supports
basic IAC negotiation, CR/CRLF lines, input echo and hidden password entry.

## RADIUS

Built-in accounts map one username to one ONU and one assigned IPv4. Access-Accept
includes Service-Type=Framed-User, Framed-Protocol=PPP, Framed-IP-Address,
Mikrotik-Rate-Limit and Acct-Interim-Interval. Unknown/disabled users or invalid
credentials receive Access-Reject. Timeout/reject faults affect new logins only.

Packets are restricted to the configured NAS management IPs. Accounting
authenticators are verified; supplied Message-Authenticators are validated.
PAP/CHAP, rate queue creation, Start/Interim/Stop and Disconnect interoperability
are exercised by the genuine CHR integration test.

In external mode no built-in RADIUS listener is started. The billing app owns
accounts, policies, accounting and disconnects. The UI disables local policy
changes in this mode. The simulator still shows kernel PPP counters and assigned
addresses but cannot invent accounting session IDs from an external server.

## Local control API

ONU ACS settings, native RPCs, connection-request authentication and reference
parameters are described in [ACS / GenieACS](acs.md). CWMP is served by the
unprivileged application and its dedicated connection-request listener.

Base URL: http://127.0.0.1:8787/api/v1. Request/response bodies are JSON except
SSE and PCAP. This is a local development service trusting loopback clients.
The server rejects other Host values and cross-origin browser access.

| Method and path | Purpose |
| --- | --- |
| GET /health, /system, /capabilities | Health, runtime prerequisites and profile limits |
| GET /images | Installed images and current download job |
| POST /images/fetch | Start download: {"version":"7.20.8"} |
| GET /labs | Stored labs |
| POST /labs | Preset: {"name":"Test","count":8}; add "empty":true for a blank canvas |
| GET /labs/ID | Full versioned topology |
| PUT /labs/ID | Save full topology including current revision; HTTP 409 on conflict |
| DELETE /labs/ID | Delete a stopped lab's document |
| GET /labs/ID/export | Download topology/accounts/faults JSON |
| POST /labs/import | Import full document as a new lab with revision 1 |
| POST /labs/ID/start, /stop | Start or stop actual network runtime |
| GET /labs/ID/runtime | Phase, progress, actual sessions, optical model, metrics and per-ONU ACS status |
| GET /labs/ID/connections | Native IPs, ports, credentials, active lab and GPON/EPON ONU identities |
| GET /labs/ID/events?after=SEQ | Up to 200 ordered events after a cursor |
| GET /labs/ID/stream | SSE "state": runtime, revision and incremental events |
| POST /labs/ID/actions | Supported canonical control action |
| POST /labs/ID/faults | Inject a fault; server assigns ID and timestamp |
| DELETE /labs/ID/faults/FAULT | Repair the specified fault |
| POST /labs/ID/exec | ONU allowlisted command: {"nodeId":"onu-0001","command":"http"} |
| POST /labs/ID/capture | PCAP: {"linkId":"drop-0001","seconds":10} |
| GET /labs/ID/terminal?node=ID | WebSocket bridge to native SSH for router/OLT |
| POST /system/helper/start | Empty JSON object; start the fixed netd command through desktop Polkit authorization; HTTP 202 while pending |

`GET /system` includes `helperOnline`, `dataDir`, `helperCommand`, and
`helperLaunch` (`available`, `starting`, optional `error`). System authentication
uses the Linux account password. Router credentials in `/connections` belong to
the selected lab; different labs can reuse the same management addresses.
CHR bootstrap enables Winbox on TCP 8291 at each start. Connect by management IP
and use the password shown or copied from the router's Integration card.

Action example:

~~~json
{"kind":"suspend","targetId":"onu-0001","value":""}
~~~

Kinds: enable, disable, power_on, power_off, authorize, deauthorize, vlan,
reboot, suspend, resume, rate, acs_inform. Authorization/VLAN/reboot target ONUs.
acs_inform queues an Inform for an ACS-enabled ONU with an active PPP session.
Billing actions accept a subscriber ID or ONU ID; rate uses a value such as
"512k/512k". Built-in billing policy is required for suspend/resume/rate.

Fault examples:

~~~json
{"kind":"cable_cut","targetType":"link","targetId":"drop-0001"}
{"kind":"attenuation","targetType":"link","targetId":"feeder-1","value":15}
{"kind":"pon_down","targetType":"port","targetId":"olt-1:pon1"}
{"kind":"power_off","targetType":"node","targetId":"onu-0001"}
{"kind":"radius_timeout","targetType":"radius","targetId":"radius"}
~~~

Also supported: admin_down on a node and radius_reject. Repairs remove only the
specified fault. Faults can be configured in a stopped snapshot; actual packet
effects require an active runtime. Optical recovery is immediate in the model,
but PPP recovery depends on discovery, LCP and RADIUS retry timers.

The schema definitions are in internal/model/model.go and validation rules in
internal/model/validate.go. Bodies are limited to 4 MiB; unknown fields and
multiple JSON values are rejected. Runtime data is transient, not imported as
live state.
