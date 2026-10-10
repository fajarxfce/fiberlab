package protocol

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"ftthlab/internal/model"
	"github.com/gosnmp/gosnmp"
)

func fixtureOptics(l *model.Lab, v *model.RuntimeView, id string, rx, tx float64) {
	for i := range l.Nodes {
		if l.Nodes[i].ID == id {
			l.Nodes[i].Config.TxDBm = tx
		}
	}
	state := v.Nodes[id]
	state.RXDBm = &rx
	v.Nodes[id] = state
}

func fixtureOffline(v *model.RuntimeView, id string) {
	state := v.Nodes[id]
	state.Status, state.OpticalUp, state.RXDBm = "offline", false, nil
	v.Nodes[id] = state
}

// Compare actual BER GET responses with an independent, sanitized device/adapter
// transcript. Literal fixture OIDs catch wrong columns, indices and value types.
func assertHSGQTranscript(t *testing.T, a *SNMPAgent, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		oid, value, ok := strings.Cut(line, " = ")
		if !ok {
			t.Fatalf("invalid fixture: %s", line)
		}
		kind, raw, _ := strings.Cut(value, ": ")
		pdu := packet(t, a, gosnmp.GetRequest, oid).Variables[0]
		switch kind {
		case "STRING", "Hex-STRING":
			var want []byte
			if kind == "STRING" {
				value, err := strconv.Unquote(raw)
				if err != nil {
					t.Fatal(err)
				}
				want = []byte(value)
			} else {
				want, err = hex.DecodeString(strings.ReplaceAll(raw, " ", ""))
				if err != nil {
					t.Fatal(err)
				}
			}
			got, ok := pdu.Value.([]byte)
			if pdu.Type != gosnmp.OctetString || !ok || !bytes.Equal(got, want) {
				t.Errorf("%s: got %v %v, want OCTET STRING %q", oid, pdu.Type, pdu.Value, want)
			}
		case "INTEGER":
			if pdu.Type != gosnmp.Integer || fmt.Sprint(pdu.Value) != raw {
				t.Errorf("%s: got %v %v, want INTEGER %s", oid, pdu.Type, pdu.Value, raw)
			}
		default:
			t.Fatalf("unsupported fixture type %q", kind)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestHSGQGPONMatchesG01IDCapture(t *testing.T) {
	a, l, v := agentFixture(8)
	fixtureOffline(v, "onu-0001")
	fixtureOptics(l, v, "onu-0002", -27, 1.93)
	fixtureOptics(l, v, "onu-0003", -17, 1.89)
	assertHSGQTranscript(t, a, "testdata/hsgq/g01id.snmp")
	for _, oid := range []string{
		".1.3.6.1.4.1.50224.3.12.3.1.4.16777472.0.0",         // offline ONT0
		".1.3.6.1.4.1.50224.3.12.3.1.4.16777472.65535.65535", // no invented OLT transceiver reading
		".1.3.6.1.4.1.50224.3.12.2.1.21.16777473",            // no fabricated ONU uptime
	} {
		if p := packet(t, a, gosnmp.GetRequest, oid).Variables[0]; p.Type != gosnmp.NoSuchObject {
			t.Errorf("unsupported/unavailable %s returned %+v", oid, p)
		}
	}
}

func TestHSGQEPONMatchesFTTHAdapterFixture(t *testing.T) {
	a, l, v := agentFixture(8)
	fixtureOffline(v, "onu-0002")
	fixtureOptics(l, v, "onu-0001", -25.52, 2.36)
	fixtureOptics(l, v, "onu-0005", -6.13, 2.5)
	assertHSGQTranscript(t, a, "testdata/hsgq/e04i.snmp")
	if p := packet(t, a, gosnmp.GetRequest, ".1.3.6.1.4.1.50224.3.3.3.1.4.16777474.0.0").Variables[0]; p.Type != gosnmp.NoSuchObject {
		t.Fatal("offline ONU retained an optical reading")
	}
}

func TestHSGQFaultsPreserveInventoryAndReportOpticalState(t *testing.T) {
	cases := []struct {
		name    string
		change  func(*model.Lab)
		offline int
	}{
		{"power", func(l *model.Lab) { l.Faults = []model.Fault{{ID: "f", Kind: "power_off", TargetID: "onu-0001"}} }, 1},
		{"drop cut", func(l *model.Lab) { l.Faults = []model.Fault{{ID: "f", Kind: "cable_cut", TargetID: "drop-0001"}} }, 1},
		{"feeder cut", func(l *model.Lab) { l.Faults = []model.Fault{{ID: "f", Kind: "cable_cut", TargetID: "feeder-1"}} }, 4},
		{"PON down", func(l *model.Lab) { l.Faults = []model.Fault{{ID: "f", Kind: "pon_down", TargetID: "olt-1:pon1"}} }, 4},
		{"LOS", func(l *model.Lab) {
			l.Faults = []model.Fault{{ID: "f", Kind: "attenuation", TargetID: "drop-0001", Value: 35}}
		}, 1},
		{"ONU disabled", func(l *model.Lab) { l.Faults = []model.Fault{{ID: "f", Kind: "admin_down", TargetID: "onu-0001"}} }, 1},
		{"deauthorized", func(l *model.Lab) {
			for i := range l.Nodes {
				if l.Nodes[i].ID == "onu-0001" {
					l.Nodes[i].Config.Registered = false
				}
			}
		}, 1},
		{"billing suspend", func(l *model.Lab) { l.Subscribers[0].Enabled = false }, 0},
		{"uplink cut", func(l *model.Lab) { l.Faults = []model.Fault{{ID: "f", Kind: "cable_cut", TargetID: "uplink-1"}} }, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, l, v := agentFixture(8)
			before := HSGQONUs(*l, *v, "olt-1")
			tc.change(l)
			v.Nodes = model.Derive(*l, true)
			if got := HSGQONUs(*l, *v, "olt-1"); !reflect.DeepEqual(got, before) {
				t.Fatal("fault changed ONU identity/index or removed inventory")
			}
			offline := 0
			for _, row := range before {
				status := packet(t, a, gosnmp.GetRequest, fmt.Sprintf("%s.8.%d", HSGQEPONInventory, row.Index)).Variables[0]
				if status.Value == 2 {
					offline++
				}
				for _, optic := range []struct {
					base  string
					index uint32
				}{{HSGQEPONOptical, row.Index}, {HSGQGPONOptical, row.GPONIndex}} {
					p := packet(t, a, gosnmp.GetRequest, fmt.Sprintf("%s.4.%d.0.0", optic.base, optic.index)).Variables[0]
					if status.Value == 2 && p.Type != gosnmp.NoSuchObject {
						t.Fatal("offline ONU has stale/zero signal")
					}
					if status.Value == 1 && p.Type != gosnmp.Integer {
						t.Fatal("online ONU lost its optical row")
					}
				}
			}
			if offline != tc.offline {
				t.Fatalf("want %d offline, got %d", tc.offline, offline)
			}
		})
	}
}

func TestHSGQAttenuationAndRepairRefreshWireResponse(t *testing.T) {
	a, l, v := agentFixture(8)
	const oid = ".1.3.6.1.4.1.50224.3.3.3.1.4.16777473.0.0"
	baseline := packet(t, a, gosnmp.GetRequest, oid).Variables[0].Value.(int)
	l.Faults = []model.Fault{{ID: "loss", Kind: "attenuation", TargetID: "drop-0001", Value: 7.25}}
	v.Nodes = model.Derive(*l, true)
	a.tableAt = time.Time{} // advance the bounded snapshot cache
	if got := packet(t, a, gosnmp.GetRequest, oid).Variables[0].Value; got != baseline-725 {
		t.Fatalf("7.25 dB attenuation: baseline %d, got %v", baseline, got)
	}
	l.Faults = nil
	v.Nodes = model.Derive(*l, true)
	a.tableAt = time.Time{}
	if got := packet(t, a, gosnmp.GetRequest, oid).Variables[0].Value; got != baseline {
		t.Fatalf("repair: %v", got)
	}
}

func TestHSGQInventoryIsolationAndOrdering(t *testing.T) {
	_, l, v := agentFixture(8)
	l.Nodes = append(l.Nodes, model.DefaultNode("olt", "olt-2", "Second OLT", 0, 0))
	for i := range l.Links {
		if l.Links[i].ID == "feeder-2" {
			l.Links[i].Source = "olt-2"
		}
	}
	v.Nodes = model.Derive(*l, true)
	a := HSGQONUs(*l, *v, "olt-1")
	b := HSGQONUs(*l, *v, "olt-2")
	if len(a) != 4 || len(b) != 4 || a[0].ID != "onu-0001" || b[0].ID != "onu-0005" || b[0].PON != 2 {
		t.Fatal("OLT/PON inventory leaked")
	}
	for i, j := 0, len(l.Nodes)-1; i < j; i, j = i+1, j-1 {
		l.Nodes[i], l.Nodes[j] = l.Nodes[j], l.Nodes[i]
	}
	if !reflect.DeepEqual(a, HSGQONUs(*l, *v, "olt-1")) || !reflect.DeepEqual(b, HSGQONUs(*l, *v, "olt-2")) {
		t.Fatal("document reordering changed indices")
	}
}

func TestHSGQUDPWalk500ONUs(t *testing.T) {
	a, l, v := agentFixture(500)
	if err := a.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	host, port, _ := net.SplitHostPort(a.conn.LocalAddr().String())
	n, _ := strconv.Atoi(port)
	client := gosnmp.GoSNMP{Target: host, Port: uint16(n), Community: "lab-read", Version: gosnmp.Version2c, Timeout: time.Second, Retries: 0, MaxRepetitions: 32}
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Conn.Close()
	wantMAC, wantSerial := map[string]string{}, map[string]string{}
	for _, row := range HSGQONUs(*l, *v, "olt-1") {
		wantMAC[fmt.Sprintf("%s.7.%d", HSGQEPONInventory, row.Index)] = row.FTTHIdentity
		wantSerial[fmt.Sprintf("%s.15.%d", HSGQGPONInventory, row.GPONIndex)] = row.Serial
	}
	for _, col := range []struct {
		oid    string
		want   map[string]string
		binary bool
	}{{HSGQEPONInventory + ".7", wantMAC, true}, {HSGQGPONInventory + ".15", wantSerial, false}} {
		pdus, err := client.BulkWalkAll(col.oid)
		if err != nil || len(pdus) != 500 {
			t.Fatalf("%s: %d rows, %v", col.oid, len(pdus), err)
		}
		for i, p := range pdus {
			value, ok := p.Value.([]byte)
			if !ok || p.Type != gosnmp.OctetString {
				t.Fatal("identity is not an OCTET STRING")
			}
			got := string(value)
			if col.binary {
				if len(value) != 6 {
					t.Fatal("MAC must contain six raw bytes")
				}
				got = strings.ToUpper(hex.EncodeToString(value))
			}
			if col.want[p.Name] != got {
				t.Fatalf("identity mismatch at %s", p.Name)
			}
			if i > 0 && !OIDLess(pdus[i-1].Name, p.Name) {
				t.Fatal("OID order is not increasing")
			}
		}
	}
}

func TestSNMPBulkLimitRetainsWholeRowsAndExhaustedColumns(t *testing.T) {
	a, _, _ := agentFixture(500)
	p := gosnmp.SnmpPacket{Version: gosnmp.Version2c, Community: "lab-read", PDUType: gosnmp.GetBulkRequest, NonRepeaters: 1, MaxRepetitions: 32}
	p.Variables = append(p.Variables, gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.1", Type: gosnmp.Null})
	for i := 0; i < 8; i++ {
		p.Variables = append(p.Variables, gosnmp.SnmpPDU{Name: HSGQEPONInventory + ".7", Type: gosnmp.Null})
	}
	p.Variables = append(p.Variables, gosnmp.SnmpPDU{Name: ".2.999", Type: gosnmp.Null})
	raw, err := p.MarshalMsg()
	if err != nil {
		t.Fatal(err)
	}
	decoder := gosnmp.GoSNMP{}
	response, err := decoder.SnmpDecodePacket(a.Handle(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Variables) > 256 || (len(response.Variables)-1)%9 != 0 || len(response.Variables) != 253 {
		t.Fatalf("partial or oversized grid: %d", len(response.Variables))
	}
	for round := 0; round < 28; round++ {
		if response.Variables[1+round*9+8].Type != gosnmp.EndOfMibView {
			t.Fatal("exhausted column lost its slot")
		}
	}
}
