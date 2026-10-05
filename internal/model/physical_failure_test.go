package model

import "testing"

func TestPoweredOffDevicesKeepPhysicalOwnership(t *testing.T) {
	for _, target := range []string{"olt-1", "onu-0001"} {
		lab := Preset(8)
		for i := range lab.Nodes {
			if lab.Nodes[i].ID == target {
				lab.Nodes[i].Config.Powered = false
			}
		}
		view := Derive(lab, true)
		first := view["onu-0001"]
		if first.OLTID != "olt-1" || first.PON != "pon1" || len(first.Path) != 3 || first.OpticalUp {
			t.Fatalf("power-off lost recoverable physical ownership: %+v", first)
		}
	}
}

func TestPONShutdownDoesNotStopItsOLTOrOtherPONs(t *testing.T) {
	lab := Preset(8)
	lab.Faults = []Fault{{ID: "fault-pon", Kind: "pon_down", TargetType: "port", TargetID: "olt-1:pon1"}}
	if err := Validate(lab); err != nil {
		t.Fatal(err)
	}
	view := Derive(lab, true)
	if view["olt-1"].Status != "online" || view["onu-0001"].OpticalUp || !view["onu-0005"].OpticalUp {
		t.Fatal("PON fault did not stay within its branch")
	}
}
