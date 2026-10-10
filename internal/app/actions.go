package app

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"ftthlab/internal/engine"
	"ftthlab/internal/model"
	"ftthlab/internal/protocol"
)

func (a *App) action(w http.ResponseWriter, r *http.Request) {
	var action model.Action
	if !decode(w, r, &action) {
		return
	}
	if action.Kind == "acs_inform" {
		l, err := a.Store.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, 404, err)
			return
		}
		if err = a.ACS.Trigger(l.ID, action.TargetID); err != nil {
			fail(w, 409, err)
			return
		}
		write(w, 200, l)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	old, err := a.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	next, err := a.Store.Get(r.Context(), old.ID)
	if err != nil {
		fail(w, 500, err)
		return
	}
	found := false
	rebootID := ""
	if action.Kind == "suspend" || action.Kind == "resume" || action.Kind == "rate" {
		if next.Radius.Mode != "builtin" {
			fail(w, 400, fmt.Errorf("subscriber policy belongs to the external RADIUS server; change it in your billing application"))
			return
		}
		for i, s := range next.Subscribers {
			if s.ID == action.TargetID || s.ONUID == action.TargetID {
				found = true
				if action.Kind == "rate" {
					next.Subscribers[i].RateLimit = action.Value
				} else {
					next.Subscribers[i].Enabled = action.Kind == "resume"
				}
				break
			}
		}
	} else {
		for i, n := range next.Nodes {
			if n.ID != action.TargetID {
				continue
			}
			found = true
			switch action.Kind {
			case "enable":
				next.Nodes[i].Config.AdminUp = true
			case "disable":
				next.Nodes[i].Config.AdminUp = false
			case "power_on":
				next.Nodes[i].Config.Powered = true
			case "power_off":
				next.Nodes[i].Config.Powered = false
			case "authorize", "deauthorize":
				if n.Kind != "onu" {
					fail(w, 400, fmt.Errorf("authorization applies only to ONUs"))
					return
				}
				next.Nodes[i].Config.Registered = action.Kind == "authorize"
			case "vlan":
				if n.Kind != "onu" {
					fail(w, 400, fmt.Errorf("use ONU service VLAN configuration"))
					return
				}
				v, err := strconv.Atoi(action.Value)
				if err != nil {
					fail(w, 400, fmt.Errorf("VLAN must be an integer"))
					return
				}
				next.Nodes[i].Config.VLAN = v
			case "reboot":
				if n.Kind != "onu" {
					fail(w, 400, fmt.Errorf("use the native RouterOS console for router reboot; simulated reboot targets an ONU"))
					return
				}
				rebootID = model.NewID("fault-")
				next.Faults = append(next.Faults, model.Fault{ID: rebootID, Kind: "power_off", TargetType: "node", TargetID: n.ID, CreatedAt: time.Now().UTC()})
			default:
				fail(w, 400, fmt.Errorf("unsupported action %q", action.Kind))
				return
			}
			break
		}
	}
	if !found {
		fail(w, 404, fmt.Errorf("action target not found"))
		return
	}
	if rebootID != "" && a.runtime(old).Phase == "running" {
		if err := a.disconnectONUSession(r.Context(), old, action.TargetID); err != nil {
			fail(w, 400, err)
			return
		}
	}
	saved, err := a.commit(r.Context(), old, next)
	if err != nil {
		fail(w, 400, err)
		return
	}
	a.event(saved.ID, "control", "info", action.TargetID, action.Kind+valueSuffix(action.Value))
	if rebootID != "" {
		go a.finishReboot(saved.ID, rebootID)
	}
	write(w, 200, saved)
}
func valueSuffix(v string) string {
	if v == "" {
		return ""
	}
	return " · " + v
}
func (a *App) finishReboot(id, faultID string) {
	select {
	case <-a.ctx.Done():
		return
	case <-time.After(3 * time.Second):
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	old, err := a.Store.Get(a.ctx, id)
	if err != nil {
		return
	}
	next, _ := a.Store.Get(a.ctx, id)
	for i, f := range next.Faults {
		if f.ID == faultID {
			next.Faults = append(next.Faults[:i], next.Faults[i+1:]...)
			ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
			_, err = a.commit(ctx, old, next)
			cancel()
			if err != nil {
				a.event(id, "control", "error", f.TargetID, "Reboot recovery failed: "+err.Error())
			} else {
				a.event(id, "control", "info", f.TargetID, "ONU power restored after reboot")
			}
			return
		}
	}
}
func (a *App) addFault(w http.ResponseWriter, r *http.Request) {
	var f model.Fault
	if !decode(w, r, &f) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	old, err := a.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	next, _ := a.Store.Get(r.Context(), old.ID)
	f.ID = model.NewID("fault-")
	f.CreatedAt = time.Now().UTC()
	next.Faults = append(next.Faults, f)
	saved, err := a.commit(r.Context(), old, next)
	if err != nil {
		fail(w, 400, err)
		return
	}
	a.event(old.ID, "fault", "warning", f.TargetID, "Injected "+f.Kind)
	write(w, 201, saved)
}
func (a *App) removeFault(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	old, err := a.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	next, _ := a.Store.Get(r.Context(), old.ID)
	found := false
	target := ""
	for i, f := range next.Faults {
		if f.ID == r.PathValue("fault") {
			target = f.TargetID
			next.Faults = append(next.Faults[:i], next.Faults[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		fail(w, 404, fmt.Errorf("fault not found"))
		return
	}
	saved, err := a.commit(r.Context(), old, next)
	if err != nil {
		fail(w, 400, err)
		return
	}
	a.event(old.ID, "fault", "info", target, "Fault repaired")
	write(w, 200, saved)
}
func (a *App) connections(w http.ResponseWriter, r *http.Request) {
	l, err := a.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	ips := model.ManagementIPs(l)
	devices := []map[string]any{}
	onus := []protocol.HSGQONU{}
	view := model.Preview(l)
	for _, n := range l.Nodes {
		if n.Kind != "router" && n.Kind != "olt" {
			continue
		}
		services := map[string]any{"ssh": map[string]any{"port": 22, "transport": "tcp"}, "snmp": map[string]any{"port": 161, "transport": "udp", "version": "2c", "community": n.Config.Community}}
		profile := n.Config.Model
		if n.Kind == "router" {
			services["winbox"] = map[string]any{"port": 8291, "transport": "tcp"}
			services["routerosApi"] = map[string]any{"port": 8728, "transport": "tcp"}
			services["disconnect"] = map[string]any{"port": 3799, "transport": "udp"}
		} else {
			services["telnet"] = map[string]any{"port": 23, "transport": "tcp"}
			profile = protocol.HSGQProfile
			onus = append(onus, protocol.HSGQONUs(l, view, n.ID)...)
		}
		devices = append(devices, map[string]any{"id": n.ID, "name": n.Label, "kind": n.Kind, "ip": ips[n.ID], "username": n.Config.Username, "password": n.Config.Password, "services": services, "profile": profile})
	}
	a.viewMu.Lock()
	activeID, phase := a.view.LabID, a.view.Phase
	a.viewMu.Unlock()
	var activeLab map[string]string
	if activeID != "" && phase != "stopped" && phase != "error" {
		activeLab = map[string]string{"id": activeID, "phase": phase, "name": activeID}
		if active, err := a.Store.Get(r.Context(), activeID); err == nil {
			activeLab["name"] = active.Name
		}
	}
	write(w, 200, map[string]any{"devices": devices, "onus": onus, "activeLab": activeLab, "radius": l.Radius, "acs": l.ACS, "cwmp": a.ACS.Snapshot(l.ID), "testOrigin": "http://198.18.0.1:8080", "subscriberPool": "172.30.0.0/22", "note": "Use device endpoints from this Linux host while this lab is running. Each lab has different router credentials even when management IPs match. HSGQ SNMP exposes GPON serials and an EPON compatibility table; the FTTH HSGQ adapter identifies ONUs by the MAC-based FTTH identity below. ONU CWMP connects from the host network to the configured ACS. The OLT CLI uses lab commands; vendor provisioning commands are unsupported."})
}
func (a *App) exec(w http.ResponseWriter, r *http.Request) {
	l, err := a.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	if a.runtime(l).Phase != "running" {
		fail(w, 409, fmt.Errorf("start the network runtime before using a client terminal"))
		return
	}
	var spec engine.ExecSpec
	if !decode(w, r, &spec) {
		return
	}
	var out map[string]string
	if err = a.Helper.Call(r.Context(), "POST", "/exec", spec, &out); err != nil {
		fail(w, 400, err)
		return
	}
	write(w, 200, out)
}
func (a *App) capture(w http.ResponseWriter, r *http.Request) {
	l, err := a.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	if a.runtime(l).Phase != "running" {
		fail(w, 409, fmt.Errorf("start the network runtime before capturing packets"))
		return
	}
	var spec engine.CaptureSpec
	if !decode(w, r, &spec) {
		return
	}
	b, err := a.Helper.Capture(r.Context(), spec)
	if err != nil {
		fail(w, 400, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.tcpdump.pcap")
	w.Header().Set("Content-Disposition", `attachment; filename="fiberlab-`+spec.LinkID+`.pcap"`)
	w.Write(b)
}
