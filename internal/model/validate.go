package model

import (
	"fmt"
	"math"
	"net"
	"regexp"
	"strconv"
	"strings"
)

var ratePattern = regexp.MustCompile(`^([0-9]+[kKmMgG]?)(/[0-9]+[kKmMgG]?)?$`)
var serialPattern = regexp.MustCompile(`^[A-Za-z]{4}[A-Fa-f0-9]{8}$`)

func Validate(l Lab) error {
	if l.ACS != nil {
		if err := ValidateACS(*l.ACS); err != nil {
			return err
		}
	}
	if l.Version != SchemaVersion {
		return fmt.Errorf("unsupported topology version %d (expected %d)", l.Version, SchemaVersion)
	}
	if !IDPattern.MatchString(l.ID) || strings.TrimSpace(l.Name) == "" || len(l.Name) > 120 {
		return fmt.Errorf("a valid lab ID and name (1–120 characters) are required")
	}
	if len(l.Nodes) > 1200 || len(l.Links) > 1600 {
		return fmt.Errorf("topology exceeds 1200 nodes or 1600 links")
	}
	nodes := map[string]Node{}
	ports := map[string]Port{}
	serials := map[string]bool{}
	macs := map[string]bool{}
	counts := map[string]int{}
	for _, n := range l.Nodes {
		if !IDPattern.MatchString(n.ID) || nodes[n.ID].ID != "" {
			return fmt.Errorf("invalid or duplicate node ID %q", n.ID)
		}
		if strings.TrimSpace(n.Label) == "" || len(n.Label) > 100 {
			return fmt.Errorf("%s: label must have 1–100 characters", n.ID)
		}
		switch n.Kind {
		case "router", "switch", "olt", "odf", "splitter", "odp", "onu":
		default:
			return fmt.Errorf("%s: unsupported node kind %q", n.ID, n.Kind)
		}
		if !finite(n.Position.X) || !finite(n.Position.Y) || math.Abs(n.Position.X) > 100000 || math.Abs(n.Position.Y) > 100000 {
			return fmt.Errorf("%s: invalid position", n.ID)
		}
		if n.Config.VLAN < 1 || n.Config.VLAN > 4094 {
			return fmt.Errorf("%s: VLAN must be 1–4094", n.ID)
		}
		if !finite(n.Config.TxDBm) || !finite(n.Config.SensitivityDBm) || n.Config.TxDBm < -10 || n.Config.TxDBm > 15 || n.Config.SensitivityDBm < -40 || n.Config.SensitivityDBm > 0 {
			return fmt.Errorf("%s: invalid optical power or sensitivity", n.ID)
		}
		if n.Kind == "splitter" || n.Kind == "odp" {
			r := n.Config.SplitRatio
			if r < 2 || r > 128 || r&(r-1) != 0 {
				return fmt.Errorf("%s: split ratio must be 2, 4, 8, 16, 32, 64 or 128", n.ID)
			}
		}
		if n.Kind == "router" {
			if n.Config.Username != "admin" {
				return fmt.Errorf("%s: initial CHR provisioning requires the admin account", n.ID)
			}
			if n.Config.MemoryMB < 512 || n.Config.MemoryMB > 8192 {
				return fmt.Errorf("%s: CHR memory must be 512–8192 MiB", n.ID)
			}
			if len(n.Config.ServiceVLANs) == 0 || len(n.Config.ServiceVLANs) > 16 {
				return fmt.Errorf("%s: configure 1–16 service VLANs", n.ID)
			}
			seen := map[int]bool{}
			for _, v := range n.Config.ServiceVLANs {
				if v < 1 || v > 4094 || seen[v] {
					return fmt.Errorf("%s: invalid or duplicate service VLAN", n.ID)
				}
				seen[v] = true
			}
		}
		if n.Kind == "router" || n.Kind == "olt" {
			if n.Config.Username == "" || len(n.Config.Password) < 8 || len(n.Config.Password) > 128 || hasControl(n.Config.Username) || hasControl(n.Config.Password) {
				return fmt.Errorf("%s: username and password of 8–128 characters are required", n.ID)
			}
			if n.Config.Community == "" || len(n.Config.Community) > 64 || hasControl(n.Config.Community) {
				return fmt.Errorf("%s: invalid SNMP community", n.ID)
			}
		}
		if n.Kind == "onu" {
			if c := n.Config.ACS; c != nil {
				if err := validateACSCredentials(c.Username, c.Password); err != nil {
					return err
				}
				if c.URL != "" {
					if err := ValidateACSURL(c.URL); err != nil {
						return fmt.Errorf("%s: %w", n.ID, err)
					}
				}
				if c.PeriodicInformSeconds != 0 && (c.PeriodicInformSeconds < 10 || c.PeriodicInformSeconds > 86400) {
					return fmt.Errorf("%s: ACS Inform interval must be 0 (inherit) or 10–86400 seconds", n.ID)
				}
			}
			if !serialPattern.MatchString(n.Config.Serial) || serials[strings.ToUpper(n.Config.Serial)] {
				return fmt.Errorf("%s: serial must be a unique GPON serial, e.g. HSGQ00000001", n.ID)
			}
			serials[strings.ToUpper(n.Config.Serial)] = true
			if n.Config.MAC != "" {
				mac, err := net.ParseMAC(n.Config.MAC)
				if err != nil || len(mac) != 6 || mac[0]&1 != 0 || mac.String() == "00:00:00:00:00:00" || macs[mac.String()] {
					return fmt.Errorf("%s: use a unique six-byte unicast MAC address", n.ID)
				}
				macs[mac.String()] = true
			}
		}
		nodes[n.ID] = n
		counts[n.Kind]++
		for _, p := range Ports(n) {
			ports[n.ID+":"+p.ID] = p
		}
	}
	if counts["onu"] > MaxONUs || counts["router"] > 8 || counts["olt"] > 8 {
		return fmt.Errorf("one lab supports at most 500 ONUs, 8 MikroTiks and 8 OLTs")
	}
	edgeIDs := map[string]bool{}
	usedPorts := map[string]bool{}
	incoming := map[string]Link{}
	for _, e := range l.Links {
		if !IDPattern.MatchString(e.ID) || edgeIDs[e.ID] {
			return fmt.Errorf("invalid or duplicate link ID %q", e.ID)
		}
		edgeIDs[e.ID] = true
		sp, ok1 := ports[e.Source+":"+e.SourcePort]
		tp, ok2 := ports[e.Target+":"+e.TargetPort]
		if !ok1 || !ok2 || e.Source == e.Target || sp.Direction != "out" || tp.Direction != "in" {
			return fmt.Errorf("%s: connect a valid output port to an input port", e.ID)
		}
		if e.Medium != sp.Medium || e.Medium != tp.Medium {
			return fmt.Errorf("%s: incompatible cable or port media", e.ID)
		}
		for _, p := range []string{e.Source + ":" + e.SourcePort, e.Target + ":" + e.TargetPort} {
			if usedPorts[p] {
				return fmt.Errorf("%s: port %s is already connected", e.ID, p)
			}
			usedPorts[p] = true
		}
		if _, ok := incoming[e.Target]; ok {
			return fmt.Errorf("%s: nodes may only have one upstream connection", e.Target)
		}
		incoming[e.Target] = e
		if !finite(e.LengthM) || !finite(e.LossDB) || e.LengthM < 0 || e.LengthM > 20000 || e.LossDB < 0 || e.LossDB > 60 {
			return fmt.Errorf("%s: cable length must be 0–20000 m and additional loss 0–60 dB", e.ID)
		}
	}
	ponCounts := map[string]int{}
	for _, n := range l.Nodes {
		visited := map[string]bool{}
		id := n.ID
		for {
			if visited[id] {
				return fmt.Errorf("cycle detected at %s; access topology must be a tree", id)
			}
			visited[id] = true
			e, ok := incoming[id]
			if !ok {
				break
			}
			if n.Kind == "onu" && nodes[e.Source].Kind == "olt" {
				ponCounts[e.Source+":"+e.SourcePort]++
			}
			id = e.Source
		}
	}
	for port, count := range ponCounts {
		if count > 128 {
			return fmt.Errorf("%s has %d ONUs; maximum is 128 per PON", port, count)
		}
	}
	users := map[string]bool{}
	addresses := map[string]bool{}
	subscriberIDs := map[string]bool{}
	onuSubs := map[string]bool{}
	_, pool, _ := net.ParseCIDR("172.30.0.0/22")
	for _, s := range l.Subscribers {
		if !IDPattern.MatchString(s.ID) || subscriberIDs[s.ID] || nodes[s.ONUID].Kind != "onu" || onuSubs[s.ONUID] {
			return fmt.Errorf("invalid/duplicate subscriber or ONU mapping %q", s.ID)
		}
		if s.Username == "" || len(s.Username) > 64 || hasControl(s.Username) || users[s.Username] || len(s.Password) < 1 || len(s.Password) > 128 || hasControl(s.Password) {
			return fmt.Errorf("subscriber usernames must be unique and credentials must not contain control characters")
		}
		ip := net.ParseIP(s.Address)
		if ip == nil || !pool.Contains(ip) || s.Address == "172.30.0.0" || s.Address == "172.30.0.1" || s.Address == "172.30.3.255" || addresses[s.Address] {
			return fmt.Errorf("%s: assign a unique address in 172.30.0.2–172.30.3.254", s.ID)
		}
		if len(s.RateLimit) > 40 || !ratePattern.MatchString(s.RateLimit) {
			return fmt.Errorf("%s: rate limit must look like 1M/1M", s.ID)
		}
		users[s.Username] = true
		addresses[s.Address] = true
		subscriberIDs[s.ID] = true
		onuSubs[s.ONUID] = true
	}
	if l.Radius.Mode != "builtin" && l.Radius.Mode != "external" {
		return fmt.Errorf("RADIUS mode must be builtin or external")
	}
	if l.Radius.Mode == "external" {
		ip := net.ParseIP(l.Radius.Address)
		if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() {
			return fmt.Errorf("external RADIUS needs an IP reachable from CHR; for a local server use 10.203.0.1, not 127.0.0.1")
		}
	}
	if l.Radius.AuthPort < 1 || l.Radius.AuthPort > 65535 || l.Radius.AccountingPort < 1 || l.Radius.AccountingPort > 65535 || l.Radius.AuthPort == l.Radius.AccountingPort || len(l.Radius.Secret) < 8 || len(l.Radius.Secret) > 128 || hasControl(l.Radius.Secret) || l.Radius.InterimSeconds < 10 || l.Radius.InterimSeconds > 3600 {
		return fmt.Errorf("invalid RADIUS ports, secret (8–128 characters), or interim interval (10–3600 seconds)")
	}
	if l.TrapAddress != "" {
		host, p, err := net.SplitHostPort(l.TrapAddress)
		port, portErr := strconv.Atoi(p)
		if err != nil || net.ParseIP(host) == nil || portErr != nil || port < 1 || port > 65535 {
			return fmt.Errorf("trap receiver must be an IP:port")
		}
	}
	if len(l.Faults) > 1500 {
		return fmt.Errorf("too many faults")
	}
	faultIDs := map[string]bool{}
	faultTargets := map[string]bool{}
	for _, f := range l.Faults {
		if !IDPattern.MatchString(f.ID) || faultIDs[f.ID] {
			return fmt.Errorf("invalid or duplicate fault ID")
		}
		faultIDs[f.ID] = true
		key := f.Kind + ":" + f.TargetID
		if faultTargets[key] {
			return fmt.Errorf("duplicate fault on %s", f.TargetID)
		}
		faultTargets[key] = true
		switch f.Kind {
		case "pon_down":
			id, port, ok := strings.Cut(f.TargetID, ":")
			p, exists := ports[f.TargetID]
			if f.TargetType != "port" || !ok || nodes[id].Kind != "olt" || !exists || p.Medium != "fiber" || !strings.HasPrefix(port, "pon") {
				return fmt.Errorf("PON fault must target an OLT port, e.g. olt-1:pon1")
			}
		case "cable_cut", "attenuation":
			if f.TargetType != "link" || !edgeIDs[f.TargetID] {
				return fmt.Errorf("fault must target an existing cable")
			}
			if f.Kind == "attenuation" && (!finite(f.Value) || f.Value < 0 || f.Value > 60) {
				return fmt.Errorf("additional attenuation must be 0–60 dB")
			}
			if f.Kind == "attenuation" {
				for _, link := range l.Links {
					if link.ID == f.TargetID && link.Medium != "fiber" {
						return fmt.Errorf("optical attenuation requires a fiber cable")
					}
				}
			}
		case "power_off", "admin_down":
			if f.TargetType != "node" || nodes[f.TargetID].ID == "" {
				return fmt.Errorf("fault must target an existing device")
			}
		case "radius_timeout", "radius_reject":
			if f.TargetType != "radius" {
				return fmt.Errorf("RADIUS fault target must be radius")
			}
			if l.Radius.Mode != "builtin" {
				return fmt.Errorf("RADIUS fault injection requires the built-in reference server")
			}
		default:
			return fmt.Errorf("unsupported fault kind %q", f.Kind)
		}
	}
	return nil
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func hasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 32 || r == 127 }) >= 0
}
