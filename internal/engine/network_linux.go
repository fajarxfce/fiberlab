//go:build linux

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ftthlab/internal/model"
	"ftthlab/internal/protocol"
	"github.com/vishvananda/netlink"
)

func (e *Engine) checkNetworks() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := command(ctx, "ip", "-j", "route", "show", "table", "all")
	if err != nil {
		return err
	}
	var routes []struct {
		Dst string `json:"dst"`
		Dev string `json:"dev"`
	}
	if err = json.Unmarshal([]byte(raw), &routes); err != nil {
		return err
	}
	for _, cidr := range []string{"10.203.0.0/24", "198.18.0.0/24"} {
		_, wanted, _ := net.ParseCIDR(cidr)
		for _, r := range routes {
			if r.Dst == "default" || r.Dst == "" {
				continue
			}
			ip, existing, err := net.ParseCIDR(r.Dst)
			if err != nil {
				ip = net.ParseIP(r.Dst)
				if ip != nil && wanted.Contains(ip) {
					return fmt.Errorf("lab network %s overlaps existing address %s on %s", cidr, r.Dst, r.Dev)
				}
				continue
			}
			if wanted.Contains(ip) || existing.Contains(wanted.IP) {
				return fmt.Errorf("lab network %s overlaps route %s on %s; no host network changes were made", cidr, r.Dst, r.Dev)
			}
		}
	}
	for _, name := range []string{"flab-mgmt", "flab-wan"} {
		if _, err := net.InterfaceByName(name); err == nil {
			return fmt.Errorf("interface %s already exists; stop its owner or run netd cleanup for a recorded stale run", name)
		}
	}
	return nil
}
func (e *Engine) iface(ctx context.Context, name string, args ...string) error {
	if _, err := command(ctx, "ip", append([]string{"link", "add", "name", name}, args...)...); err != nil {
		return err
	}
	e.journal.Interfaces = append(e.journal.Interfaces, name)
	return e.writeJournal()
}
func (e *Engine) bridge(ctx context.Context, name string, vlan bool) error {
	args := []string{"type", "bridge"}
	if vlan {
		args = append(args, "vlan_filtering", "1", "vlan_default_pvid", "0")
	}
	if err := e.iface(ctx, name, args...); err != nil {
		return err
	}
	_, err := command(ctx, "ip", "link", "set", name, "up")
	return err
}
func (e *Engine) attach(ctx context.Context, iface, bridge string) error {
	if _, err := command(ctx, "ip", "link", "set", iface, "master", bridge); err != nil {
		return err
	}
	_, err := command(ctx, "ip", "link", "set", iface, "up")
	return err
}
func (e *Engine) tap(ctx context.Context, name string) error {
	if _, err := command(ctx, "ip", "tuntap", "add", "dev", name, "mode", "tap", "user", strconv.Itoa(e.UID)); err != nil {
		return err
	}
	e.journal.Interfaces = append(e.journal.Interfaces, name)
	if err := e.writeJournal(); err != nil {
		return err
	}
	_, err := command(ctx, "ip", "link", "set", name, "up")
	return err
}
func (e *Engine) trunk(ctx context.Context, iface string) error {
	_, err := command(ctx, "bridge", "vlan", "add", "dev", iface, "vid", "2-4094")
	if err != nil {
		return err
	}
	_, err = command(ctx, "bridge", "vlan", "add", "dev", iface, "vid", "1")
	return err
}
func (e *Engine) buildNetwork(ctx context.Context) error {
	if err := e.bridge(ctx, "flab-mgmt", false); err != nil {
		return err
	}
	if _, err := command(ctx, "ip", "addr", "add", "10.203.0.1/24", "dev", "flab-mgmt"); err != nil {
		return err
	}
	if err := e.bridge(ctx, "flab-wan", false); err != nil {
		return err
	}
	if _, err := command(ctx, "ip", "addr", "add", "198.18.0.1/24", "dev", "flab-wan"); err != nil {
		return err
	}
	ipMap := model.ManagementIPs(e.lab)
	nodes := append([]model.Node(nil), e.lab.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	for index, node := range nodes {
		if node.Kind != "olt" && node.Kind != "switch" {
			continue
		}
		name := fmt.Sprintf("flab-b%d", index)
		if err := e.bridge(ctx, name, node.Kind == "olt"); err != nil {
			return err
		}
		e.bridges[node.ID] = name
		if node.Kind == "olt" {
			if _, err := command(ctx, "ip", "addr", "add", ipMap[node.ID]+"/32", "dev", "flab-mgmt"); err != nil {
				return err
			}
			e.oltIPs[node.ID] = true
		}
	}
	for i, node := range e.lab.SortedNodes("router") {
		router := &Router{Node: node, Address: ipMap[node.ID]}
		for port := 0; port < 6; port++ {
			tap := fmt.Sprintf("flab-r%dt%d", i, port)
			if err := e.tap(ctx, tap); err != nil {
				return err
			}
			router.Taps = append(router.Taps, tap)
			if port == 0 {
				if err := e.attach(ctx, tap, "flab-mgmt"); err != nil {
					return err
				}
			}
			if port == 1 {
				if err := e.attach(ctx, tap, "flab-wan"); err != nil {
					return err
				}
			}
		}
		e.routers[node.ID] = router
	}
	for i, link := range e.lab.Links {
		if link.Medium != "ethernet" {
			continue
		}
		e.linkUp[link.ID] = true
		source, _ := e.lab.Node(link.Source)
		target, _ := e.lab.Node(link.Target)
		if source.Kind == "router" {
			port, err := strconv.Atoi(strings.TrimPrefix(link.SourcePort, "lan"))
			if err != nil || port < 1 || port > 4 {
				return fmt.Errorf("invalid router port")
			}
			tap := e.routers[source.ID].Taps[port+1]
			if err = e.attach(ctx, tap, e.bridges[target.ID]); err != nil {
				return err
			}
			if target.Kind == "olt" {
				if err = e.trunk(ctx, tap); err != nil {
					return err
				}
			}
			e.linkInterfaces[link.ID] = tap
		} else {
			left, right := fmt.Sprintf("flab-e%da", i), fmt.Sprintf("flab-e%db", i)
			if err := e.iface(ctx, left, "type", "veth", "peer", "name", right); err != nil {
				return err
			}
			if err := e.attach(ctx, left, e.bridges[source.ID]); err != nil {
				return err
			}
			if err := e.attach(ctx, right, e.bridges[target.ID]); err != nil {
				return err
			}
			if target.Kind == "olt" {
				if err := e.trunk(ctx, right); err != nil {
					return err
				}
			}
			e.linkInterfaces[link.ID] = left
		}
	}
	return nil
}

func (e *Engine) apply(ctx context.Context, lab model.Lab) error {
	old := e.Lab()
	states := model.Derive(lab, true)
	previous := model.Derive(old, true)
	for _, edge := range lab.Links {
		if edge.Medium != "ethernet" {
			continue
		}
		iface := e.linkInterfaces[edge.ID]
		if iface == "" {
			continue
		}
		up := states[edge.Source].Status == "online" && states[edge.Target].Status == "online"
		for _, f := range lab.Faults {
			if f.TargetID == edge.ID && f.Kind == "cable_cut" {
				up = false
			}
		}
		if e.linkUp[edge.ID] == up {
			continue
		}
		state := "up"
		if !up {
			state = "down"
		}
		if _, err := command(ctx, "ip", "link", "set", iface, state); err != nil {
			return err
		}
		e.linkUp[edge.ID] = up
	}
	for _, node := range lab.SortedNodes("onu") {
		client := e.clients[node.ID]
		if client == nil {
			continue
		}
		state := states[node.ID]
		up := state.OpticalUp && node.Config.Registered && node.Config.AdminUp && node.Config.Powered
		linkState := "down"
		if up {
			linkState = "up"
		}
		if client.LinkUp != up {
			if _, err := command(ctx, "ip", "link", "set", client.Interface, linkState); err != nil {
				return err
			}
			client.LinkUp = up
		}
		before, _ := old.Node(node.ID)
		if before.Config.VLAN != node.Config.VLAN {
			if _, err := command(ctx, "bridge", "vlan", "del", "dev", client.Interface, "vid", strconv.Itoa(before.Config.VLAN)); err != nil {
				return err
			}
			if _, err := command(ctx, "bridge", "vlan", "add", "dev", client.Interface, "vid", strconv.Itoa(node.Config.VLAN), "pvid", "untagged"); err != nil {
				return err
			}
		}
	}
	for _, node := range lab.SortedNodes("olt") {
		up := states[node.ID].Status == "online"
		current := e.oltIPs[node.ID]
		if up != current {
			op := "del"
			if up {
				op = "add"
			}
			if _, err := command(ctx, "ip", "addr", op, model.ManagementIPs(lab)[node.ID]+"/32", "dev", "flab-mgmt"); err != nil {
				return err
			}
			e.oltIPs[node.ID] = up
		}
	}
	for _, node := range lab.SortedNodes("router") {
		router := e.routers[node.ID]
		if router == nil || router.Process == nil {
			continue
		}
		paused := states[node.ID].Status != "online"
		if router.Paused != paused {
			op := "cont"
			if paused {
				op = "stop"
			}
			if err := QMP(ctx, router.QMP, op); err != nil {
				return err
			}
			router.Paused = paused
		}
	}
	// Publish configuration before disconnecting, so reconnect attempts observe
	// the new RADIUS policy, never a transient re-accept of a suspended customer.
	e.mu.Lock()
	e.lab = lab
	e.view.Nodes = states
	e.mu.Unlock()
	for i, sub := range lab.Subscribers {
		if i >= len(old.Subscribers) {
			continue
		}
		before := old.Subscribers[i]
		if (before.Enabled && !sub.Enabled) || before.RateLimit != sub.RateLimit {
			router, ok := model.RouterFor(lab, sub.ONUID)
			if !ok {
				continue
			}
			e.mu.Lock()
			account := e.acct[sub.Username]
			e.mu.Unlock()
			disconnectCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			err := protocol.Disconnect(disconnectCtx, model.ManagementIPs(lab)[router.ID], lab.Radius.Secret, sub.Username, account.SessionID)
			cancel()
			if err != nil {
				active := account.Kind == "Start" || account.Kind == "Interim"
				for _, session := range e.Snapshot().Sessions {
					if session.ONUID == sub.ONUID && session.Status == "connected" {
						active = true
					}
				}
				if active {
					return fmt.Errorf("could not disconnect active subscriber %s: %w", sub.Username, err)
				}
				e.Event("billing", "info", sub.Username, "Policy applies on next login; no active session was observed")
			} else {
				e.Event("billing", "info", sub.Username, "NAS acknowledged Disconnect-Request")
			}
		}
	}
	for id, state := range states {
		before := previous[id]
		if state.Status != before.Status || state.Reason != before.Reason {
			level := "info"
			if state.Status != "online" {
				level = "warning"
			}
			node, _ := lab.Node(id)
			e.Event("device", level, node.Label, state.Status+" · "+state.Reason)
			if lab.TrapAddress != "" && node.Kind == "onu" && state.OLTID != "" && states[state.OLTID].Status == "online" {
				olt, _ := lab.Node(state.OLTID)
				_ = protocol.SendONUTrap(model.ManagementIPs(lab)[olt.ID], lab.TrapAddress, olt.Config.Community, id, state.Status, time.Since(*e.view.StartedAt))
			}
		}
	}
	return nil
}

// Recover removes only resources named in this helper's journal, and only if
// their names are in the application's fixed namespace. Never flush host rules.
func (e *Engine) Recover(ctx context.Context) error {
	b, err := os.ReadFile(filepath.Join(e.Dir, "active.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var j Journal
	if err = json.Unmarshal(b, &j); err != nil {
		return err
	}
	for _, pid := range j.PIDs {
		if j.StartTimes[pid] == "" || processStartTime(pid) != j.StartTimes[pid] {
			continue
		}
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err == nil && (strings.Contains(string(cmdline), e.Dir) || strings.Contains(string(cmdline), "ftl-")) {
			p, err := os.FindProcess(pid)
			if err == nil {
				_ = p.Kill()
			}
		}
	}
	return e.removeNetwork(ctx, j)
}

func (e *Engine) removeNetwork(ctx context.Context, journal Journal) error {
	var failures []error
	for _, ns := range journal.Namespaces {
		if !strings.HasPrefix(ns, "ftl-") || strings.ContainsAny(ns, "/\\") {
			failures = append(failures, fmt.Errorf("invalid namespace in journal"))
			continue
		}
		if _, err := os.Stat(filepath.Join("/run/netns", ns)); os.IsNotExist(err) {
			continue
		}
		if _, err := command(ctx, "ip", "netns", "delete", ns); err != nil {
			failures = append(failures, err)
		}
	}
	for i := len(journal.Interfaces) - 1; i >= 0; i-- {
		name := journal.Interfaces[i]
		if !strings.HasPrefix(name, "flab-") {
			failures = append(failures, fmt.Errorf("invalid interface in journal"))
			continue
		}
		link, err := netlink.LinkByName(name)
		var missing netlink.LinkNotFoundError
		if errors.As(err, &missing) {
			continue
		}
		if err == nil {
			err = netlink.LinkDel(link)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("remove %s: %w", name, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("cleanup incomplete; ownership journal retained: %w", errors.Join(failures...))
	}
	if err := os.Remove(filepath.Join(e.Dir, "active.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
