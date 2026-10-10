package protocol

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ftthlab/internal/model"
)

// Opt-in contract test against an independently built FTTH server boot JAR.
// No production database, live router, privileged helper or vendor device is
// involved. CI still runs the wire-fixture, fault and 500-ONU UDP tests.
func TestFTTHHSGQAdapterInterop(t *testing.T) {
	bootJar := os.Getenv("FTTH_BOOT_JAR")
	if bootJar == "" {
		t.Skip("set FTTH_BOOT_JAR to run the actual FTTH HSGQ adapter over UDP (JDK 21)")
	}
	bootJar, err := filepath.Abs(bootJar)
	if err != nil {
		t.Fatal(err)
	}
	classes := t.TempDir()
	compile := exec.Command("javac", "--release", "21", "-d", classes, "../../scripts/HsgqInterop.java")
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile probe: %v\n%s", err, output)
	}
	cases := []struct {
		name   string
		count  int
		change func(*model.Lab)
	}{
		{"500 online", 500, func(*model.Lab) {}},
		{"ONU power failure", 8, func(l *model.Lab) { l.Faults = []model.Fault{{ID: "f", Kind: "power_off", TargetID: "onu-0001"}} }},
		{"feeder cut", 8, func(l *model.Lab) { l.Faults = []model.Fault{{ID: "f", Kind: "cable_cut", TargetID: "feeder-1"}} }},
		{"all optics absent", 1, func(l *model.Lab) { l.Faults = []model.Fault{{ID: "f", Kind: "power_off", TargetID: "onu-0001"}} }},
		{"attenuation", 8, func(l *model.Lab) {
			l.Faults = []model.Fault{{ID: "f", Kind: "attenuation", TargetID: "drop-0001", Value: 7.25}}
		}},
		{"billing suspend", 8, func(l *model.Lab) { l.Subscribers[0].Enabled = false }},
		{"uplink failure", 8, func(l *model.Lab) { l.Faults = []model.Fault{{ID: "f", Kind: "cable_cut", TargetID: "uplink-1"}} }},
		{"missing MAC", 8, func(l *model.Lab) {
			for i := range l.Nodes {
				if l.Nodes[i].Kind == "onu" {
					l.Nodes[i].Config.MAC = ""
				}
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, l, v := agentFixture(tc.count)
			tc.change(l)
			v.Nodes = model.Derive(*l, true)
			if err := a.Listen("127.0.0.1:0"); err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			host, port, _ := net.SplitHostPort(a.conn.LocalAddr().String())
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			probe := exec.CommandContext(ctx, "java", "-cp", classes, "HsgqInterop", bootJar, host, port)
			probe.Env = append(os.Environ(), "FTTH_SNMP_COMMUNITY=lab-read")
			var stderr bytes.Buffer
			probe.Stderr = &stderr
			output, err := probe.Output()
			if err != nil {
				t.Fatalf("FTTH adapter: %v\n%s", err, stderr.String())
			}
			readings := map[string][]string{}
			for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
				fields := strings.Split(line, "\t")
				if len(fields) != 5 || readings[fields[0]] != nil {
					t.Fatalf("invalid/duplicate reading: %q", line)
				}
				readings[fields[0]] = fields
			}
			if len(readings) != tc.count {
				t.Fatalf("got %d, want %d ONUs", len(readings), tc.count)
			}
			for _, n := range l.SortedNodes("onu") {
				identity := strings.ToUpper(strings.ReplaceAll(model.ONUMAC(n), ":", ""))
				r, ok := readings[identity]
				if !ok {
					t.Fatalf("ONU %s missing from FTTH readings", n.ID)
				}
				state := v.Nodes[n.ID]
				if r[2] != strings.ToUpper(state.PON) {
					t.Fatalf("%s: incorrect PON %s", n.ID, r[2])
				}
				if state.Status != "online" || !n.Config.Registered {
					if r[1] != "OFFLINE" || r[3] != "null" || r[4] != "null" {
						t.Fatalf("offline ONU %s: %v", n.ID, r)
					}
					continue
				}
				rx, rxErr := strconv.ParseFloat(r[3], 64)
				tx, txErr := strconv.ParseFloat(r[4], 64)
				if r[1] != "ONLINE" || rxErr != nil || txErr != nil || rx != *state.RXDBm || tx != n.Config.TxDBm {
					t.Fatalf("optical reading for %s: %v, expected RX %v / TX %v", n.ID, r, *state.RXDBm, n.Config.TxDBm)
				}
			}
		})
	}
}
