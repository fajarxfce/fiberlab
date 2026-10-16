package engine

import (
	"net/netip"
	"testing"
)

func TestRadiusUsesExistingRouterRoutes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		address string
		route   map[string]string
		covered bool
	}{
		{"internet default", "198.51.100.42", map[string]string{"dst-address": "0.0.0.0/0", "routing-table": "main"}, true},
		{"VPN subnet", "10.208.67.1", map[string]string{"dst-address": "10.208.67.0/24"}, true},
		{"VPN reconnecting", "10.208.67.1", map[string]string{"dst-address": "10.208.67.0/24", "active": "false"}, true},
		{"manual host route", "198.51.100.42", map[string]string{"dst-address": "198.51.100.42/32"}, true},
		{"connected WAN", "198.18.0.1", map[string]string{"dst-address": "198.18.0.0/24"}, true},
		{"only management connected", "198.51.100.42", map[string]string{"dst-address": "10.203.0.0/24"}, false},
		{"disabled default", "198.51.100.42", map[string]string{"dst-address": "0.0.0.0/0", "disabled": "true"}, false},
		{"different routing table", "198.51.100.42", map[string]string{"dst-address": "0.0.0.0/0", "routing-table": "other-vrf"}, false},
		{"different VPN subnet", "10.208.68.1", map[string]string{"dst-address": "10.208.67.0/24"}, false},
		{"IPv6 default", "198.51.100.42", map[string]string{"dst-address": "::/0"}, false},
		{"missing destination", "198.51.100.42", map[string]string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := routeCoversAddress(tc.route, netip.MustParseAddr(tc.address)); got != tc.covered {
				t.Fatalf("route %v covers %s: got %v, want %v", tc.route, tc.address, got, tc.covered)
			}
		})
	}
}
