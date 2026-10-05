#!/usr/bin/env python3
"""Real GenieACS + real PPP runtime check. Start scripts/acs_fixture.cjs first.

Only a newly created verification lab and its own ACS identities are changed.
The local fixture is disposable; no existing ACS deployment is used.
"""
import argparse
import json
import os
from pathlib import Path
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = "InternetGatewayDevice."
WLAN = ROOT + "LANDevice.1.WLANConfiguration.1."
MANAGEMENT = ROOT + "ManagementServer."


def http(base, path, method="GET", value=None):
    request = urllib.request.Request(base + path, method=method,
        data=None if value is None else json.dumps(value).encode(),
        headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(request, timeout=35) as response:
            data = response.read()
            return response.status, json.loads(data) if data else None
    except urllib.error.HTTPError as error:
        raise RuntimeError(f"{method} {path.split('?')[0]} returned HTTP {error.code}: {error.read(2048).decode(errors='replace')}") from error


def wait(label, check, seconds=90):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        result = check()
        if result:
            print("PASS · " + label, flush=True)
            return result
        time.sleep(0.5)
    raise AssertionError("Timed out: " + label)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--app", default="http://127.0.0.1:8787/api/v1")
    parser.add_argument("--nbi", default="http://127.0.0.1:17557")
    parser.add_argument("--keep-lab", action="store_true")
    args = parser.parse_args()
    report = {"status": "running", "startedAt": time.time(), "checks": [], "failures": [],
              "fixture": "GenieACS 1.2.16 / MongoDB 7.0.16", "realPPPClients": 3, "managedONUs": 2}
    stamp = time.strftime("%Y%m%d-%H%M%S")
    output = Path("artifacts") / ("acs-genieacs-" + stamp + ".json")
    output.parent.mkdir(exist_ok=True)
    lab_id = None
    def save_report():
        output.write_text(json.dumps(report, indent=2) + "\n")
        output.chmod(0o600)
    def app(path, method="GET", value=None): return http(args.app, path, method, value)[1]
    def runtime(): return app("/labs/" + lab_id + "/runtime")
    def acs_devices():
        query = urllib.parse.urlencode({"query": json.dumps({"_id": {"$regex": report["serialSuffix"] + "$"}})})
        return http(args.nbi, "/devices?" + query)[1]
    def device(device_id):
        return next(d for d in acs_devices() if d["_id"] == device_id)
    def value(device_id, path):
        data = device(device_id)
        for part in path.rstrip(".").split("."):
            data = data.get(part, {})
        return data.get("_value")
    def task(device_id, spec):
        if spec.get("name") == "refreshObject":
            # GenieACS NBI uses object paths without the CWMP trailing dot.
            spec = {**spec, "objectName": spec["objectName"].rstrip(".")}
        path = "/devices/" + urllib.parse.quote(device_id, safe="") + "/tasks?connection_request&timeout=20000"
        code, result = http(args.nbi, path, "POST", spec)
        if code != 200:
            raise AssertionError(f"GenieACS task did not finish: HTTP {code}, {result}")
        return result
    def check(name):
        report["checks"].append(name)
        save_report()
    try:
        assert app("/system")["helperOnline"], "Network helper is not running"
        http(args.nbi, "/devices")
        lab = app("/labs", "POST", {"name": "Verification · GenieACS", "count": 3})
        lab_id = lab["id"]
        report["labID"] = lab_id
        report["serialSuffix"] = lab_id.removeprefix("lab-")
        images = app("/images")["images"]
        assert images, "An official CHR image must be installed"
        lab["imageId"] = images[0]["id"]
        lab["acs"] = {"enabled": True, "url": "http://127.0.0.1:17547",
            "username": "fiberlab-test", "password": "fiberlab-test-password", "periodicInformSeconds": 10,
            "connectionRequestListen": "127.0.0.1:17548", "connectionRequestUrl": "http://127.0.0.1:17548",
            "connectionRequestUsername": "fiberlab-cr", "connectionRequestPassword": "fiberlab-cr-password"}
        for node in lab["nodes"]:
            if node["id"] == "onu-0002":
                node["config"]["acs"] = {"disabled": False, "url": lab["acs"]["url"],
                    "username": "fiberlab-test", "password": "deliberately-wrong"}
            elif node["id"] == "onu-0003":
                node["config"]["acs"] = {"disabled": True}
        lab = app("/labs/" + lab_id, "PUT", lab)
        save_report()
        app("/labs/" + lab_id + "/start", "POST", {})
        wait("three real PPP sessions", lambda: runtime()["metrics"]["activeSessions"] == 3, 180)
        wait("valid ACS credentials register one ONU", lambda: len(acs_devices()) == 1)
        wait("wrong per-ONU credentials are rejected", lambda: runtime().get("acs", {}).get("onu-0002", {}).get("status") == "error")
        assert "onu-0003" not in runtime().get("acs", {})
        check("digest_authentication_and_per_onu_opt_out")
        lab = app("/labs/" + lab_id)
        next(n for n in lab["nodes"] if n["id"] == "onu-0002")["config"]["acs"]["password"] = "fiberlab-test-password"
        app("/labs/" + lab_id, "PUT", lab)
        wait("per-ONU credentials recover without restarting PPP", lambda: len(acs_devices()) == 2)
        status = runtime()["acs"]
        device_one = status["onu-0001"]["deviceId"]
        device_two = status["onu-0002"]["deviceId"]
        report["deviceIDs"] = [device_one, device_two]
        assert device_one != device_two
        check("unique_stable_identity_and_bootstrap_inform")
        task(device_one, {"name": "refreshObject", "objectName": ROOT})
        assert value(device_one, ROOT + "DeviceInfo.Manufacturer") == "Fiberlab"
        session_one = next(s for s in runtime()["sessions"] if s["onuId"] == "onu-0001")
        assert value(device_one, ROOT + "WANDevice.1.WANConnectionDevice.1.WANPPPConnection.1.ExternalIPAddress") == session_one["address"]
        check("native_genieacs_connection_request_and_parameter_discovery")
        task(device_one, {"name": "setParameterValues", "parameterValues": [
            [WLAN + "SSID", "Rumah ACS Test", "xsd:string"],
            [WLAN + "Enable", False, "xsd:boolean"],
            [WLAN + "KeyPassphrase", "wifi-test-secret", "xsd:string"],
            [MANAGEMENT + "PeriodicInformInterval", 10, "xsd:unsignedInt"],
        ]})
        task(device_one, {"name": "refreshObject", "objectName": ROOT})
        assert value(device_one, WLAN + "SSID") == "Rumah ACS Test"
        assert value(device_one, WLAN + "Enable") is False
        assert value(device_one, WLAN + "KeyPassphrase") == ""
        assert value(device_one, MANAGEMENT + "Password") == ""
        check("set_parameter_values_wifi_interval_and_write_only_passwords")
        count = runtime()["acs"]["onu-0001"]["informCount"]
        wait("periodic Inform", lambda: runtime()["acs"]["onu-0001"]["informCount"] > count, 25)
        check("periodic_inform")
        before = {s["onuId"]: s["sessionId"] for s in runtime()["sessions"]}
        task(device_one, {"name": "reboot"})
        wait("ACS reboot creates a new real PPP session", lambda: any(s["onuId"] == "onu-0001" and s["status"] == "connected" and s.get("sessionId") != before["onu-0001"] for s in runtime()["sessions"]))
        for session in runtime()["sessions"]:
            if session["onuId"] != "onu-0001": assert session["sessionId"] == before[session["onuId"]]
        wait("ONU returns to ACS after reboot", lambda: runtime()["acs"]["onu-0001"]["status"] == "online")
        check("acs_reboot_real_ppp_disconnect_and_bounded_recovery")
        boot_before_cut = device(device_two).get("_lastBoot")
        app("/labs/" + lab_id + "/faults", "POST", {"kind": "cable_cut", "targetType": "link", "targetId": "drop-0002"})
        wait("cut ONU stops contacting ACS", lambda: runtime()["acs"]["onu-0002"]["status"] == "offline")
        count = runtime()["acs"]["onu-0002"]["informCount"]
        time.sleep(13)
        assert runtime()["acs"]["onu-0002"]["informCount"] == count
        assert next(s for s in runtime()["sessions"] if s["onuId"] == "onu-0001")["status"] == "connected"
        lab = app("/labs/" + lab_id)
        cut = next(f for f in lab["faults"] if f["kind"] == "cable_cut")
        app("/labs/" + lab_id + "/faults/" + cut["id"], "DELETE")
        wait("repaired ONU reconnects to ACS", lambda: runtime()["acs"]["onu-0002"]["informCount"] > count)
        assert device(device_two).get("_lastBoot") == boot_before_cut
        check("optical_outage_pauses_acs_and_repair_resumes")
        app("/labs/" + lab_id + "/actions", "POST", {"kind": "power_off", "targetId": "onu-0002"})
        wait("ONU power off pauses ACS", lambda: runtime()["acs"]["onu-0002"]["status"] == "offline")
        app("/labs/" + lab_id + "/actions", "POST", {"kind": "power_on", "targetId": "onu-0002"})
        wait("power restore reports BOOT to GenieACS", lambda: device(device_two).get("_lastBoot") != boot_before_cut)
        check("power_cycle_boot_event_distinct_from_fiber_repair")
        app("/labs/" + lab_id + "/stop", "POST", {})
        app("/labs/" + lab_id + "/start", "POST", {})
        wait("runtime restart restores both managed ONUs", lambda: len(runtime().get("acs", {})) == 2 and all(s.get("status") == "online" for s in runtime()["acs"].values()), 180)
        task(device_one, {"name": "refreshObject", "objectName": WLAN})
        assert value(device_one, WLAN + "SSID") == "Rumah ACS Test"
        assert value(device_one, WLAN + "Enable") is False
        assert {s["deviceId"] for s in runtime()["acs"].values()} == {device_one, device_two}
        check("sqlite_parameter_persistence_and_stable_identity_after_restart")
        task(device_one, {"name": "factoryReset"})
        wait("factory-reset ONU returns to ACS", lambda: runtime()["acs"]["onu-0001"].get("status") == "offline", 30)
        wait("factory reset recovers", lambda: runtime()["acs"]["onu-0001"].get("status") == "online", 90)
        task(device_one, {"name": "refreshObject", "objectName": WLAN})
        assert value(device_one, WLAN + "SSID").startswith("Fiberlab-")
        assert value(device_one, WLAN + "Enable") is True
        check("factory_reset_resets_managed_parameters_and_reboots_only_onu")
        report["status"] = "passed"
    except (Exception, KeyboardInterrupt) as error:
        report["status"] = "failed"
        report["failures"].append(str(error) or "cancelled")
        print("FAIL · " + str(error), flush=True)
    finally:
        if lab_id:
            try:
                app("/labs/" + lab_id + "/stop", "POST", {})
                assert runtime()["phase"] == "stopped"
                check("runtime_clean_stop")
                if report["status"] == "passed" and not args.keep_lab:
                    app("/labs/" + lab_id, "DELETE")
            except Exception as error:
                report["status"] = "failed"
                report["failures"].append("cleanup: " + str(error))
        report["finishedAt"] = time.time()
        save_report()
        print("Report: " + str(output), flush=True)
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
