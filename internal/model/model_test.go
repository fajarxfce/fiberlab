package model

import (
	"fmt"
	"strings"
	"testing"
)

func TestPresetsAndManagementIdentity(t *testing.T) {
	for _, count := range []int{1, 8, 32, 100, 500} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			l := Preset(count)
			if err := Validate(l); err != nil {
				t.Fatal(err)
			}
			if len(l.Subscribers) != count {
				t.Fatalf("wanted %d subscribers", count)
			}
			states := Derive(l, true)
			for _, n := range l.SortedNodes("onu") {
				s := states[n.ID]
				if s.Status != "online" || !s.OpticalUp || !s.ServicePathUp {
					t.Fatalf("%s has invalid baseline state %+v", n.ID, s)
				}
			}
			ips := ManagementIPs(l)
			for i, j := 0, len(l.Nodes)-1; i < j; i, j = i+1, j-1 {
				l.Nodes[i], l.Nodes[j] = l.Nodes[j], l.Nodes[i]
			}
			for id, ip := range ManagementIPs(l) {
				if ips[id] != ip {
					t.Fatal("reordering moved a management endpoint")
				}
			}
		})
	}
}
func TestFiberFailureHasBoundedBlastRadius(t *testing.T) {
	l := Preset(8)
	l.Faults = append(l.Faults, Fault{ID: "cut", Kind: "cable_cut", TargetType: "link", TargetID: "feeder-1"})
	states := Derive(l, true)
	for i := 1; i <= 8; i++ {
		s := states[fmt.Sprintf("onu-%04d", i)]
		if i <= 4 {
			if s.Status != "los" || s.OpticalUp {
				t.Fatalf("branch 1 ONU remained optically up: %+v", s)
			}
		} else if s.Status != "online" {
			t.Fatalf("unrelated branch affected: %+v", s)
		}
	}
	if states["olt-1"].Status != "online" {
		t.Fatal("cut feeder must not power off OLT")
	}
	l.Faults = nil
	if s := Derive(l, true)["onu-0001"]; s.Status != "online" {
		t.Fatal("repair did not restore optical state")
	}
}
func TestUplinkAndBillingDoNotCreateLOS(t *testing.T) {
	l := Preset(8)
	l.Faults = []Fault{{ID: "cut", Kind: "cable_cut", TargetType: "link", TargetID: "uplink-1"}}
	s := Derive(l, true)["onu-0001"]
	if s.Status != "online" || !s.OpticalUp || s.ServicePathUp {
		t.Fatalf("Ethernet uplink failure conflated with optical LOS: %+v", s)
	}
	l.Faults = nil
	l.Subscribers[0].Enabled = false
	s = Derive(l, true)["onu-0001"]
	if s.Status != "online" || !s.OpticalUp {
		t.Fatal("billing suspend produced LOS")
	}
	v := Preview(l)
	if v.Metrics.ActiveSessions != 0 {
		t.Fatal("stopped preview fabricated a session")
	}
	for _, s := range v.Sessions {
		if s.Status != "stopped" || s.RXBytes != 0 || s.Address != "" {
			t.Fatal("preview fabricated telemetry")
		}
	}
}
func TestOpticalBudgetAndAuthorization(t *testing.T) {
	l := Preset(8)
	before := Derive(l, true)["onu-0001"]
	l.Faults = []Fault{{ID: "loss", Kind: "attenuation", TargetType: "link", TargetID: "drop-0001", Value: 35}}
	after := Derive(l, true)["onu-0001"]
	if after.Status != "los" || after.RXDBm == nil || *after.RXDBm >= *before.RXDBm {
		t.Fatal("optical budget not applied")
	}
	l.Faults = nil
	for i, n := range l.Nodes {
		if n.ID == "onu-0001" {
			l.Nodes[i].Config.Registered = false
		}
	}
	s := Derive(l, true)["onu-0001"]
	if s.Status != "unregistered" || !s.OpticalUp || s.ServicePathUp {
		t.Fatalf("authorization is independent of light level: %+v", s)
	}
}
func TestValidationRejectsBrokenContracts(t *testing.T) {
	cases := []struct {
		name     string
		change   func(*Lab)
		contains string
	}{
		{"duplicate port", func(l *Lab) {
			e := l.Links[len(l.Links)-1]
			e.ID = "duplicate"
			e.Target = "onu-0001"
			l.Links = append(l.Links, e)
		}, "already connected"},
		{"wrong medium", func(l *Lab) { l.Links[0].Medium = "fiber" }, "incompatible"},
		{"duplicate serial", func(l *Lab) {
			var serial string
			for i, n := range l.Nodes {
				if n.Kind == "onu" {
					if serial == "" {
						serial = n.Config.Serial
					} else {
						l.Nodes[i].Config.Serial = serial
						return
					}
				}
			}
		}, "unique GPON"},
		{"invalid VLAN", func(l *Lab) { l.Nodes[0].Config.VLAN = 4095 }, "VLAN"},
		{"duplicate address", func(l *Lab) { l.Subscribers[1].Address = l.Subscribers[0].Address }, "unique address"},
		{"missing version", func(l *Lab) { l.Version = 0 }, "unsupported topology"},
		{"path injection", func(l *Lab) { l.Nodes[0].ID = "../../etc" }, "invalid"},
		{"external loopback", func(l *Lab) { l.Radius.Mode = "external"; l.Radius.Address = "127.0.0.1" }, "reachable"},
		{"duplicate faults", func(l *Lab) {
			l.Faults = []Fault{{ID: "f1", Kind: "cable_cut", TargetType: "link", TargetID: "uplink-1"}, {ID: "f2", Kind: "cable_cut", TargetType: "link", TargetID: "uplink-1"}}
		}, "duplicate fault"},
		{"cycle", func(l *Lab) {
			l.Nodes = append(l.Nodes, DefaultNode("switch", "sa", "Switch A", 0, 0), DefaultNode("switch", "sb", "Switch B", 0, 0))
			l.Links = append(l.Links, Link{ID: "ea", Source: "sa", SourcePort: "port1", Target: "sb", TargetPort: "uplink", Medium: "ethernet"}, Link{ID: "eb", Source: "sb", SourcePort: "port1", Target: "sa", TargetPort: "uplink", Medium: "ethernet"})
		}, "cycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := Preset(8)
			tc.change(&l)
			err := Validate(l)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("expected %q, got %v", tc.contains, err)
			}
		})
	}
}
func BenchmarkDerive500(b *testing.B) {
	l := Preset(500)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Derive(l, true)
	}
}
