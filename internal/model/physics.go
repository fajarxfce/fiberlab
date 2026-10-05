package model

import (
	"fmt"
	"math"
)

// Derive separates optical availability from Ethernet reachability and PPP state.
// No session or traffic counters are fabricated by the topology model.
func Derive(l Lab, running bool) map[string]NodeState {
	nodes := map[string]Node{}
	incoming := map[string]Link{}
	faults := map[string][]Fault{}
	out := map[string]NodeState{}
	for _, n := range l.Nodes {
		nodes[n.ID] = n
	}
	for _, e := range l.Links {
		incoming[e.Target] = e
	}
	for _, f := range l.Faults {
		faults[f.TargetID] = append(faults[f.TargetID], f)
	}
	deviceUp := func(n Node) (bool, string) {
		if !n.Config.Powered {
			return false, "Device has no power"
		}
		if !n.Config.AdminUp {
			return false, "Administratively disabled"
		}
		for _, f := range faults[n.ID] {
			if f.Kind == "power_off" {
				return false, "Power loss / dying gasp"
			}
			if f.Kind == "admin_down" {
				return false, "Administratively disabled"
			}
		}
		return true, ""
	}
	linkUp := func(e Link) bool {
		for _, f := range faults[e.ID] {
			if f.Kind == "cable_cut" {
				return false
			}
		}
		return true
	}
	var servicePath func(string, map[string]bool) bool
	servicePath = func(id string, seen map[string]bool) bool {
		if seen[id] {
			return false
		}
		seen[id] = true
		n, ok := nodes[id]
		if !ok {
			return false
		}
		if up, _ := deviceUp(n); !up {
			return false
		}
		if n.Kind == "router" {
			return true
		}
		e, ok := incoming[id]
		if !ok || !linkUp(e) {
			return false
		}
		return servicePath(e.Source, seen)
	}
	var optical func(string, map[string]bool) NodeState
	optical = func(id string, seen map[string]bool) NodeState {
		if s, ok := out[id]; ok {
			return s
		}
		n := nodes[id]
		s := NodeState{ID: id, Status: "los", Reason: "No optical path to an OLT", Path: []string{}}
		if seen[id] {
			return s
		}
		seen[id] = true
		if up, reason := deviceUp(n); !up {
			s.Status = "offline"
			s.Reason = reason
			out[id] = s
			return s
		}
		if n.Kind == "router" || n.Kind == "switch" {
			s.Status = "online"
			s.Reason = "Ethernet device enabled"
			s.ServicePathUp = servicePath(id, map[string]bool{})
			out[id] = s
			return s
		}
		if n.Kind == "olt" {
			p := n.Config.TxDBm
			s.Status = "online"
			s.Reason = "OLT optical transmitter enabled"
			s.OpticalUp = true
			s.OLTID = id
			s.RXDBm = &p
			s.ServicePathUp = servicePath(id, map[string]bool{})
			out[id] = s
			return s
		}
		e, ok := incoming[id]
		if !ok || e.Medium != "fiber" {
			out[id] = s
			return s
		}
		p := optical(e.Source, seen)
		s.OLTID = p.OLTID
		s.PON = p.PON
		s.Path = append(append([]string{}, p.Path...), e.ID)
		if nodes[e.Source].Kind == "olt" {
			s.PON = e.SourcePort
			for _, f := range faults[e.Source+":"+e.SourcePort] {
				if f.Kind == "pon_down" {
					s.Reason = "PON port disabled: " + e.SourcePort
					out[id] = s
					return s
				}
			}
		}
		if !linkUp(e) {
			s.Reason = "Fiber cut: " + e.ID
			out[id] = s
			return s
		}
		if !p.OpticalUp || p.RXDBm == nil {
			s.Reason = "Upstream optical loss: " + nodes[e.Source].Label
			out[id] = s
			return s
		}
		loss := e.LossDB + e.LengthM/1000*0.25
		for _, f := range faults[e.ID] {
			if f.Kind == "attenuation" {
				loss += f.Value
			}
		}
		parent := nodes[e.Source]
		if parent.Kind == "splitter" || parent.Kind == "odp" {
			loss += SplitterLoss(parent.Config.SplitRatio)
		}
		power := Round(*p.RXDBm - loss)
		s.RXDBm = &power
		if n.Kind == "onu" && power < n.Config.SensitivityDBm {
			s.Reason = fmt.Sprintf("Optical level %.2f dBm below %.2f dBm receiver sensitivity", power, n.Config.SensitivityDBm)
			out[id] = s
			return s
		}
		s.OpticalUp = true
		s.Status = "online"
		s.Reason = "Optical path available"
		s.ServicePathUp = p.ServicePathUp
		if n.Kind == "onu" && !n.Config.Registered {
			s.Status = "unregistered"
			s.Reason = "ONU discovered; authorization required"
			s.ServicePathUp = false
		}
		out[id] = s
		return s
	}
	for _, n := range l.Nodes {
		optical(n.ID, map[string]bool{})
	}
	ips := ManagementIPs(l)
	for id, s := range out {
		// Physical ownership survives a power failure. It is needed to create
		// disconnected clients at startup and later restore them without a rebuild.
		if n := nodes[id]; n.Kind != "router" && n.Kind != "switch" {
			current := id
			path := []string{}
			seen := map[string]bool{}
			for !seen[current] {
				seen[current] = true
				if nodes[current].Kind == "olt" {
					s.OLTID = current
					for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
						path[left], path[right] = path[right], path[left]
					}
					s.Path = path
					break
				}
				edge, ok := incoming[current]
				if !ok || edge.Medium != "fiber" {
					break
				}
				path = append(path, edge.ID)
				if nodes[edge.Source].Kind == "olt" {
					s.PON = edge.SourcePort
				}
				current = edge.Source
			}
		}
		s.ManagementIP = ips[id]
		if !running && s.Status == "online" {
			s.Status = "ready"
			s.Reason = "Topology ready; runtime is stopped"
		}
		out[id] = s
	}
	return out
}
func SplitterLoss(ratio int) float64 {
	if ratio < 2 {
		return 0
	}
	return Round(10*math.Log10(float64(ratio)) + 0.5*math.Log2(float64(ratio)))
}
func RouterFor(l Lab, id string) (Node, bool) {
	seen := map[string]bool{}
	for !seen[id] {
		seen[id] = true
		n, ok := l.Node(id)
		if !ok {
			return Node{}, false
		}
		if n.Kind == "router" {
			return n, true
		}
		e, ok := l.Incoming(id)
		if !ok {
			return Node{}, false
		}
		id = e.Source
	}
	return Node{}, false
}
func Preview(l Lab) RuntimeView {
	sessions := []Session{}
	for _, s := range l.Subscribers {
		sessions = append(sessions, Session{ONUID: s.ONUID, Username: s.Username, Status: "stopped"})
	}
	return RuntimeView{LabID: l.ID, Phase: "stopped", Message: "Start the network runtime to establish real PPPoE sessions.", Nodes: Derive(l, false), Sessions: sessions, Metrics: Metrics{ConfiguredSessions: len(sessions)}}
}
