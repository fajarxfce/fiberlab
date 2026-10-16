#!/usr/bin/env python3
"""Verify a running lab's real ONU/RouterOS traffic; optionally observe RADIUS on the wire."""
import argparse
import json
import os
from pathlib import Path
import socket
import struct
import time

from verify_runtime import RouterOS, api


class AccountingCapture:
    """Observe only Accounting-Requests; never store raw packets or credentials."""
    def __init__(self, port, users, routers):
        self.port, self.users, self.routers = port, set(users), set(routers)
        self.samples = {user: [] for user in users}
        self.socket = socket.socket(socket.AF_PACKET, socket.SOCK_RAW, socket.htons(3))
        self.socket.bind(("flab-mgmt", 0))
        self.socket.settimeout(0.1)

    def poll(self):
        deadline = time.monotonic() + 0.2
        while time.monotonic() < deadline:
            try:
                frame = self.socket.recv(4096)
            except socket.timeout:
                return
            if len(frame) < 42 or frame[12:14] != b"\x08\x00" or frame[23] != 17:
                continue
            if socket.inet_ntoa(frame[26:30]) not in self.routers:
                continue
            udp = 14 + (frame[14] & 15) * 4
            if len(frame) < udp + 28 or int.from_bytes(frame[udp+2:udp+4], "big") != self.port:
                continue
            data = frame[udp+8:]
            size = int.from_bytes(data[2:4], "big")
            if data[0] != 4 or size < 20 or len(data) < size:
                continue
            fields, offset = {}, 20
            while offset + 2 <= size:
                kind, length = data[offset:offset+2]
                if length < 2 or offset + length > size:
                    break
                fields[kind] = data[offset+2:offset+length]
                offset += length
            user = fields.get(1, b"").decode(errors="replace")
            if user not in self.users or fields.get(40) != struct.pack(">I", 3):
                continue
            integer = lambda key: int.from_bytes(fields.get(key, b"\0"), "big")
            sample = {"at": time.time(), "sessionId": fields.get(44, b"").decode(errors="replace"),
                      "uploadBytes": integer(42) + (integer(52) << 32),
                      "downloadBytes": integer(43) + (integer(53) << 32)}
            previous = self.samples[user]
            if not previous or any(sample[k] != previous[-1][k] for k in ("sessionId", "uploadBytes", "downloadBytes")):
                previous.append(sample)

    def increased(self):
        return all(len(rows) >= 2 and rows[-1]["sessionId"] == rows[0]["sessionId"]
                   and rows[-1]["downloadBytes"] > rows[0]["downloadBytes"]
                   and rows[-1]["uploadBytes"] > rows[0]["uploadBytes"]
                   for rows in self.samples.values())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default="http://127.0.0.1:8787")
    parser.add_argument("--lab-id", required=True)
    parser.add_argument("--seconds", type=int, default=20)
    parser.add_argument("--radius", action="store_true", help="Capture real Interim-Updates; requires CAP_NET_RAW in the helper network namespace")
    parser.add_argument("--output", default="artifacts/traffic-native.json")
    args = parser.parse_args()
    if args.seconds < 10:
        parser.error("--seconds must be at least 10")
    os.umask(0o077)
    prefix = "/labs/" + args.lab_id
    call = lambda path, method="GET", body=None: api(args.url, path, method, body)
    lab = call(prefix)
    state = call(prefix + "/runtime")
    if state["phase"] != "running" or state.get("traffic", {}).get("state") == "running":
        raise RuntimeError("Start the lab and stop any existing traffic job before this probe")
    onus = [s["onuId"] for s in lab["subscribers"]]
    if not onus or any(s["status"] != "connected" for s in state["sessions"]):
        raise RuntimeError("All selected subscribers must have connected PPPoE sessions")
    connections = call(prefix + "/connections")
    devices = [d for d in connections["devices"] if d["kind"] == "router"]
    report = {"labId": lab["id"], "status": "running", "checks": [], "startedAt": time.time()}
    routers, capture, job = [], None, None
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    try:
        for device in devices:
            routers.append(RouterOS(device["ip"], device["username"], device["password"]))

        def counters():
            rows = {}
            for router in routers:
                for row in router.call("/interface/print", "=stats=", "=.proplist=name,type,rx-byte,tx-byte"):
                    if row.get("type") == "pppoe-in":
                        rows[row["name"]] = {"uploadBytes": int(row["rx-byte"]), "downloadBytes": int(row["tx-byte"])}
            return {s["onuId"]: rows["<pppoe-" + s["username"] + ">"] for s in lab["subscribers"]}

        before = counters()
        seconds = args.seconds
        if args.radius:
            if lab["radius"]["mode"] != "builtin":
                raise RuntimeError("Use this RADIUS capture assertion with built-in accounting; external intervals are owned by the NAS/FTTH configuration")
            capture = AccountingCapture(lab["radius"]["accountingPort"], [s["username"] for s in lab["subscribers"]], [d["ip"] for d in devices])
            seconds = max(seconds, 2 * lab["radius"]["interimSeconds"] + 10)
        config = {"onuIds": onus, "downloadKbps": 64, "uploadKbps": 16, "durationSeconds": seconds + 20}
        started = call(prefix + "/traffic", "POST", config)
        job = started["traffic"]["startedAt"]
        deadline, next_log = time.monotonic() + seconds, 0
        saw_rates = False
        while time.monotonic() < deadline:
            if capture:
                capture.poll()
            state = call(prefix + "/runtime")
            if state["phase"] != "running" or state["traffic"]["startedAt"] != job:
                raise RuntimeError("The lab or traffic job changed during verification")
            if any(s["status"] != "connected" for s in state["sessions"]):
                raise RuntimeError("A PPPoE session disconnected during steady traffic")
            saw_rates |= all(s.get("rxBitsPerSecond", 0) > 0 and s.get("txBitsPerSecond", 0) > 0 for s in state["sessions"])
            if time.monotonic() >= next_log:
                print("Traffic: %d/%d streams, observed download %.1f Kbps / upload %.1f Kbps" % (
                    sum(o["state"] == "streaming" for o in state["traffic"]["onus"]), len(onus),
                    sum(s.get("rxBitsPerSecond", 0) for s in state["sessions"])/1000,
                    sum(s.get("txBitsPerSecond", 0) for s in state["sessions"])/1000), flush=True)
                next_log = time.monotonic() + 15
            time.sleep(0.8)
        after = counters()
        for onu in onus:
            if any(after[onu][direction] <= before[onu][direction] + 1024 for direction in ("downloadBytes", "uploadBytes")):
                raise RuntimeError("Native CHR PPP counters did not increase in both directions for " + onu)
        if not saw_rates:
            raise RuntimeError("Runtime never observed nonzero rates for every ONU")
        report.update(nativeBefore=before, nativeAfter=after, elapsedSeconds=seconds)
        report["checks"].extend(["native_CHR_PPP_download_and_upload_counters_increased", "kernel_PPP_rates_observed_for_every_ONU"])
        if capture:
            report["radiusSamples"] = capture.samples
            if not capture.increased():
                raise RuntimeError("Did not observe two increasing real RADIUS Interim-Updates for every subscriber")
            report["checks"].append("real_RADIUS_Interim_Update_input_and_output_octets_increased")
        call(prefix + "/traffic", "DELETE")
        state = call(prefix + "/runtime")
        # App status can be one poll behind the DELETE response.
        if state["traffic"]["state"] == "running":
            time.sleep(1.2)
            state = call(prefix + "/runtime")
        if state["traffic"]["state"] != "stopped" or state["phase"] != "running":
            raise RuntimeError("Stopping traffic did not preserve the running lab")
        report["checks"].append("traffic_stopped_without_stopping_lab")
        report["status"] = "passed"
        print("Passed: %d ONU streams recorded by native RouterOS%s" % (len(onus), " and RADIUS accounting" if capture else ""), flush=True)
    except BaseException as error:
        report.update(status="failed", error=str(error))
        raise
    finally:
        if job:
            try:
                current = call(prefix + "/runtime").get("traffic")
                if current and current["startedAt"] == job and current["state"] == "running":
                    call(prefix + "/traffic", "DELETE")
            except Exception:
                pass
        if capture:
            capture.socket.close()
        for router in routers:
            router.socket.close()
        output.write_text(json.dumps(report, indent=2) + "\n")
        if os.geteuid() == 0:
            parent = output.parent.stat()
            os.chown(output, parent.st_uid, parent.st_gid)


if __name__ == "__main__":
    main()
