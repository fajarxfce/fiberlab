#!/usr/bin/env python3
"""Exercise a real running Fiberlab helper. Python standard library only."""
import argparse
import csv
import json
import os
from pathlib import Path
import socket
import struct
import sys
import time
from urllib.error import HTTPError
from urllib.request import Request, urlopen


def arguments():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default="http://127.0.0.1:8787")
    parser.add_argument("--onus", type=int, choices=[1, 8, 32, 100, 500], default=8)
    parser.add_argument("--seconds", type=int, default=60)
    parser.add_argument("--faults", action="store_true")
    parser.add_argument("--keep-lab", action="store_true",
                        help="Keep the stopped lab document after a successful test")
    parser.add_argument("--output", default="artifacts")
    args = parser.parse_args()
    if args.seconds < 30:
        parser.error("--seconds must be at least 30 to observe interim accounting")
    return args


def api(base, path, method="GET", body=None):
    data = None if body is None else json.dumps(body).encode()
    req = Request(base.rstrip("/") + "/api/v1" + path, data=data, method=method,
                  headers={"Content-Type": "application/json"})
    try:
        with urlopen(req, timeout=60) as response:
            return json.load(response)
    except HTTPError as error:
        detail = error.read().decode(errors="replace")
        raise RuntimeError("API %s %s: %s" % (method, path, detail)) from error


def ros_length(length):
    if length < 0x80:
        return bytes([length])
    if length < 0x4000:
        return struct.pack(">H", length | 0x8000)
    if length < 0x200000:
        return struct.pack(">I", length | 0xC00000)[1:]
    if length < 0x10000000:
        return struct.pack(">I", length | 0xE0000000)
    return b"\xf0" + struct.pack(">I", length)


class RouterOS:
    """Minimal native RouterOS API client; no simulator API for NAS assertions."""
    def __init__(self, address, username, password):
        self.socket = socket.create_connection((address, 8728), timeout=10)
        try:
            self.call("/login", "=name=" + username, "=password=" + password)
        except BaseException:
            self.socket.close()
            raise

    def read(self, length):
        data = bytearray()
        while len(data) < length:
            block = self.socket.recv(length - len(data))
            if not block:
                raise RuntimeError("RouterOS API disconnected")
            data.extend(block)
        return bytes(data)

    def word(self):
        first = self.read(1)[0]
        if first < 0x80:
            size = first
        elif first < 0xC0:
            size = ((first & 0x3F) << 8) | self.read(1)[0]
        elif first < 0xE0:
            size = ((first & 0x1F) << 16) | int.from_bytes(self.read(2), "big")
        elif first < 0xF0:
            size = ((first & 0x0F) << 24) | int.from_bytes(self.read(3), "big")
        elif first == 0xF0:
            size = int.from_bytes(self.read(4), "big")
        else:
            raise RuntimeError("Unsupported RouterOS API word encoding")
        if size > 1 << 20:
            raise RuntimeError("Oversized RouterOS API word")
        return self.read(size).decode() if size else ""

    def call(self, *words):
        wire = b"".join(ros_length(len(w.encode())) + w.encode() for w in words) + b"\0"
        self.socket.sendall(wire)
        rows = []
        while True:
            sentence = []
            while True:
                word = self.word()
                if not word:
                    break
                sentence.append(word)
            if not sentence:
                continue
            attributes = {}
            for word in sentence[1:]:
                if word.startswith("="):
                    key, _, value = word[1:].partition("=")
                    attributes[key] = value
            if sentence[0] in ("!trap", "!fatal"):
                raise RuntimeError("RouterOS: " + attributes.get("message", sentence[0]))
            if sentence[0] == "!done":
                return rows
            if sentence[0] == "!re":
                rows.append(attributes)


def run(args):
    os.umask(0o077)
    output = Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    stem = "runtime-%d-%s" % (args.onus, time.strftime("%Y%m%d-%H%M%S"))
    report = {"status": "running", "requestedONUs": args.onus, "steadySeconds": args.seconds,
              "checks": [], "probes": 0, "failures": [], "startedAt": time.time()}
    report_path = output / (stem + ".json")
    csv_path = output / (stem + ".csv")
    lab = None
    api_call = lambda path, method="GET", body=None: api(args.url, path, method, body)
    started = False
    exit_code = 0
    try:
        if not api_call("/system")["helperOnline"]:
            raise RuntimeError("Network helper is offline. Start the sudo netd command shown in Runtime settings.")
        images = api_call("/images")["images"]
        if not images:
            raise RuntimeError("Import or download the official CHR image first.")
        lab = api_call("/labs", "POST", {"name": "Verification · %d ONU" % args.onus, "count": args.onus})
        lab["imageId"] = images[0]["id"]
        lab["radius"]["interimSeconds"] = 10
        lab = api_call("/labs/" + lab["id"], "PUT", lab)
        prefix = "/labs/" + lab["id"]
        report["labId"] = lab["id"]
        report["imageVersion"] = images[0]["version"]
        report["imageSHA256"] = images[0]["sha256"]
        connections = api_call(prefix + "/connections")
        routers = [d for d in connections["devices"] if d["kind"] == "router"]

        def state():
            view = api_call(prefix + "/runtime")
            if view["phase"] in ("error", "unavailable"):
                raise RuntimeError(view.get("message", view["phase"]))
            return view

        def wait_for(description, predicate, seconds=60):
            deadline = time.monotonic() + seconds
            last = None
            while time.monotonic() < deadline:
                last = state()
                if predicate(last):
                    return last
                time.sleep(1)
            raise RuntimeError("%s timed out (phase=%s, observed sessions=%s)" %
                               (description, last["phase"], last["metrics"]["activeSessions"]))

        def nas_count():
            seen = set()
            expected = {s["username"]: s["address"] for s in lab["subscribers"]}
            for router in routers:
                nas = RouterOS(router["ip"], router["username"], router["password"])
                try:
                    rows = nas.call("/ppp/active/print", "=.proplist=.id,name,address")
                    for row in rows:
                        if row["name"] not in expected or row["name"] in seen:
                            raise RuntimeError("CHR has an unexpected or duplicate subscriber session")
                        seen.add(row["name"])
                        if row["address"] != expected[row["name"]]:
                            raise RuntimeError("CHR assigned an unexpected subscriber IP")
                finally:
                    nas.socket.close()
            return len(seen)

        def fully_connected(view):
            return (view["phase"] == "running" and
                    view["metrics"]["activeSessions"] == args.onus and
                    all(s["status"] == "connected" and s.get("address") and s.get("sessionId")
                        for s in view["sessions"]))

        print("Starting %d real PPPoE clients; waiting for CHR and namespaces..." % args.onus, flush=True)
        api_call(prefix + "/start", "POST", {})
        started = True
        wait_for("all real PPPoE sessions", fully_connected, 300)
        if nas_count() != args.onus:
            raise RuntimeError("CHR active-session count differs from the kernel")
        report["checks"].append("kernel_and_native_CHR_session_counts")
        wait_for("interim accounting for every subscriber",
                 lambda v: all(s.get("lastAccounting") == "Interim" for s in v["sessions"]), 45)
        report["checks"].append("accounting_for_every_subscriber")

        def action(kind, target, value=""):
            return api_call(prefix + "/actions", "POST", {"kind": kind, "targetId": target, "value": value})

        if args.faults:
            baseline = state()
            first = lab["subscribers"][0]["onuId"]
            first_state = baseline["nodes"][first]
            branch = {s["onuId"] for s in lab["subscribers"]
                      if baseline["nodes"][s["onuId"]].get("pon") == first_state["pon"]}
            cases = [
                ("drop_cut", "cable_cut", "link", "drop-0001", 0, {first}),
                ("feeder_cut", "cable_cut", "link", "feeder-1", 0, branch),
                ("optical_attenuation", "attenuation", "link", "drop-0001", 50, {first}),
                ("pon_down", "pon_down", "port", "olt-1:pon1", 0, branch),
            ]
            for label, kind, target_type, target, value, affected in cases:
                before = {s["onuId"]: s["sessionId"] for s in state()["sessions"]}
                updated = api_call(prefix + "/faults", "POST",
                                   {"kind": kind, "targetType": target_type, "targetId": target, "value": value})
                fault = next(f for f in updated["faults"] if f["kind"] == kind and f["targetId"] == target)
                down = wait_for(label, lambda v: v["metrics"]["activeSessions"] == args.onus - len(affected))
                for subscriber in down["sessions"]:
                    onu = subscriber["onuId"]
                    if onu in affected:
                        if down["nodes"][onu]["opticalUp"]:
                            raise RuntimeError(label + ": affected ONU still has optical availability")
                    elif not down["nodes"][onu]["opticalUp"] or subscriber["sessionId"] != before[onu]:
                        raise RuntimeError(label + ": unaffected subscriber was interrupted")
                api_call(prefix + "/faults/" + fault["id"], "DELETE")
                wait_for(label + " repair", fully_connected)
                report["checks"].append(label + "_bounded_recovery")
                print("Verified " + label, flush=True)

            action("vlan", first, "999")
            down = wait_for("VLAN mismatch", lambda v: v["metrics"]["activeSessions"] == args.onus - 1)
            if not down["nodes"][first]["opticalUp"]:
                raise RuntimeError("VLAN mismatch incorrectly produced optical LOS")
            action("vlan", first, "100")
            wait_for("VLAN repair", fully_connected)
            action("suspend", first)
            down = wait_for("billing disconnect", lambda v: v["metrics"]["activeSessions"] == args.onus - 1)
            if not down["nodes"][first]["opticalUp"]:
                raise RuntimeError("Billing suspension incorrectly produced optical LOS")
            action("resume", first)
            wait_for("billing resume", fully_connected)
            report["checks"].extend(["VLAN_failure_without_LOS", "billing_suspend_resume_without_LOS"])

        baseline = wait_for("settled accounting before the steady interval",
                            lambda v: fully_connected(v) and
                            all(s.get("lastAccounting") == "Interim" for s in v["sessions"]), 45)
        session_ids = {s["onuId"]: s["sessionId"] for s in baseline["sessions"]}

        # At least one small real HTTP request per subscriber, then one per
        # minute. This stays within the free CHR interface limit.
        interval = min(60.0, args.seconds) / args.onus
        first_counters = baseline["metrics"]
        steady_started = time.monotonic()
        report["steadyStartedAt"] = time.time()
        until = steady_started + args.seconds
        next_probe = next_sample = next_native = steady_started
        minimum = args.onus
        maximum_rss = 0
        client_index = 0
        probed_clients = set()
        native = args.onus
        with csv_path.open("w", newline="") as file:
            writer = csv.writer(file)
            writer.writerow(["timestamp", "active", "nativeActive", "appRSS", "helperRSS", "qemuRSS", "pppdRSS", "rxBytes", "txBytes"])
            while time.monotonic() < until:
                now = time.monotonic()
                if now >= next_probe:
                    subscriber = lab["subscribers"][client_index % args.onus]
                    result = api_call(prefix + "/exec", "POST", {"nodeId": subscriber["onuId"], "command": "http"})
                    if "HTTP 200" not in result["output"] or "Fiberlab test origin" not in result["output"]:
                        raise RuntimeError("Real HTTP probe failed for " + subscriber["onuId"] + ": " + result["output"])
                    report["probes"] += 1
                    probed_clients.add(subscriber["onuId"])
                    client_index += 1
                    next_probe += interval
                if now >= next_native:
                    native = nas_count()
                    if native != args.onus:
                        raise RuntimeError("CHR lost a session during the steady interval")
                    next_native = now + 15
                if now >= next_sample:
                    view = state()
                    metrics = view["metrics"]
                    active = metrics["activeSessions"]
                    minimum = min(minimum, active)
                    if active != args.onus:
                        raise RuntimeError("Kernel lost a session during the steady interval")
                    for session in view["sessions"]:
                        if session.get("sessionId") != session_ids.get(session["onuId"]):
                            raise RuntimeError("Subscriber session restarted during the steady interval: " + session["onuId"])
                    sizes = [metrics.get(k, 0) for k in ("goRssBytes", "workerRssBytes", "qemuRssBytes", "pppRssBytes")]
                    maximum_rss = max(maximum_rss, sum(sizes))
                    writer.writerow([time.time(), active, native, *sizes, metrics["rxBytes"], metrics["txBytes"]])
                    file.flush()
                    next_sample = now + 5
                    print("Active %d/%d · probes %d · aggregate RSS %.1f MiB" %
                          (active, args.onus, report["probes"], sum(sizes) / 1048576), flush=True)
                time.sleep(min(0.05, max(0.001, min(next_probe, next_sample, next_native, until) - time.monotonic())))
        report["actualSteadySeconds"] = time.monotonic() - steady_started
        report["sampledClients"] = len(probed_clients)
        if len(probed_clients) != args.onus:
            raise RuntimeError("The steady interval ended before every subscriber completed an HTTP probe")
        final = state()["metrics"]
        if final["rxBytes"] <= first_counters["rxBytes"] or final["txBytes"] <= first_counters["txBytes"]:
            raise RuntimeError("Traffic counters did not increase")
        report.update(status="passed", minimumActive=minimum, peakAggregateRSSBytes=maximum_rss)
        report["checks"].append("steady_actual_HTTP_and_incrementing_counters")
        report["checks"].append("steady_unchanged_subscriber_session_ids")
    except KeyboardInterrupt:
        report["status"] = "cancelled"
        report["failures"].append("Interrupted by the operator")
        exit_code = 130
    except Exception as error:
        report["status"] = "failed"
        report["failures"].append(str(error))
        print(str(error), file=sys.stderr, flush=True)
        exit_code = 1
    finally:
        if lab and started:
            try:
                stopped = api_call("/labs/" + lab["id"] + "/stop", "POST", {})
                if stopped["phase"] != "stopped":
                    raise RuntimeError(stopped.get("message", "runtime did not stop"))
                report["checks"].append("runtime_clean_stop")
            except Exception as error:
                report["status"] = "failed"
                report["failures"].append("cleanup: " + str(error))
                exit_code = 1
        if lab and not args.keep_lab and report["status"] == "passed":
            try:
                api_call("/labs/" + lab["id"], "DELETE")
            except Exception as error:
                report["status"] = "failed"
                report["failures"].append("delete lab: " + str(error))
                exit_code = 1
        report["finishedAt"] = time.time()
        report_path.write_text(json.dumps(report, indent=2) + "\n")
        print("Report: " + str(report_path), flush=True)
    return exit_code


if __name__ == "__main__":
    sys.exit(run(arguments()))
