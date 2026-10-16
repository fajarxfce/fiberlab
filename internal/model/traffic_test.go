package model

import "testing"

func TestTrafficValidation(t *testing.T) {
	valid := TrafficConfig{ONUIDs: []string{"onu-1"}, DownloadKbps: 64, UploadKbps: 16, DurationSeconds: 300}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*TrafficConfig){
		func(c *TrafficConfig) { c.ONUIDs = nil },
		func(c *TrafficConfig) { c.ONUIDs = []string{"onu-1", "onu-1"} },
		func(c *TrafficConfig) { c.DownloadKbps = -1 },
		func(c *TrafficConfig) { c.DownloadKbps, c.UploadKbps = 0, 0 },
		func(c *TrafficConfig) { c.UploadKbps = MaxTrafficKbps + 1 },
		func(c *TrafficConfig) { c.ONUIDs = []string{"onu-1", "onu-2"}; c.DownloadKbps = MaxTrafficKbps / 2 },
		func(c *TrafficConfig) { c.DurationSeconds = -1 },
		func(c *TrafficConfig) { c.DurationSeconds = 86401 },
	} {
		config := valid
		change(&config)
		if config.Validate() == nil {
			t.Fatalf("accepted invalid traffic: %+v", config)
		}
	}
	valid.DurationSeconds, valid.UploadKbps = 0, 0
	if err := valid.Validate(); err != nil {
		t.Fatal("download-only until stopped rejected:", err)
	}
}
