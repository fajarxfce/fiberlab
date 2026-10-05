package app

import (
	"ftthlab/internal/model"
	"testing"
)

func TestRebootSelectsOnlyOwningONUSession(t *testing.T) {
	lab := model.Preset(2)
	node, _ := lab.Node("onu-0001")
	sub := lab.Subscribers[0]
	row := map[string]string{".id": "*123", "name": sub.Username, "address": sub.Address, "service": "pppoe", "caller-id": node.Config.MAC}
	if !matchesONUSession(row, node, sub, sub.Address) {
		t.Fatal("own ONU session did not match")
	}
	for key, wrong := range map[string]string{".id": "", "name": "another-customer", "address": lab.Subscribers[1].Address, "service": "l2tp", "caller-id": "02:46:54:00:00:02"} {
		changed := map[string]string{}
		for k, v := range row {
			changed[k] = v
		}
		changed[key] = wrong
		if matchesONUSession(changed, node, sub, sub.Address) {
			t.Fatalf("reboot could disconnect a mismatched %s", key)
		}
	}
}
