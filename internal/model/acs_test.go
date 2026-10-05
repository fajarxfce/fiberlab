package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestACSValidationAndHelperBoundary(t *testing.T) {
	for _, bad := range []string{"file:///etc/passwd", "http://user:password@localhost:7547", "http://localhost:99999", "https://host/#fragment", "not-a-url"} {
		if ValidateACSURL(bad) == nil {
			t.Fatalf("accepted invalid ACS endpoint %q", bad)
		}
	}
	c := DefaultACS()
	c.Enabled = true
	if err := ValidateACS(c); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ACSConfig){
		func(c *ACSConfig) { c.ConnectionRequestListen = "127.0.0.1:80" },
		func(c *ACSConfig) { c.ConnectionRequestPassword = "" },
		func(c *ACSConfig) { c.ConnectionRequestURL = "http://0.0.0.0:7548" },
		func(c *ACSConfig) { c.PeriodicInformSeconds = 0 },
	} {
		bad := c
		mutate(&bad)
		if ValidateACS(bad) == nil {
			t.Fatal("accepted invalid enabled ACS configuration")
		}
	}
	l := Preset(1)
	l.ACS = &c
	for i := range l.Nodes {
		if l.Nodes[i].Kind == "onu" {
			l.Nodes[i].Config.ACS = &ONUACSConfig{URL: "http://localhost:7547", Password: "app-only-secret"}
		}
	}
	if err := Validate(l); err != nil {
		t.Fatal(err)
	}
	wire := NetworkLab(l)
	b, _ := json.Marshal(wire)
	if strings.Contains(string(b), `"acs"`) || strings.Contains(string(b), "app-only-secret") {
		t.Fatal("application ACS data leaked into privileged helper payload")
	}
	if l.ACS == nil {
		t.Fatal("helper normalization mutated the stored lab")
	}
	for _, n := range l.Nodes {
		if n.Kind == "onu" && n.Config.ACS == nil {
			t.Fatal("helper normalization mutated a node")
		}
	}
}
