package protocol

import (
	"io"
	"log"
	"net"
	"strconv"
	"testing"
	"time"

	"ftthlab/internal/model"
	"github.com/gosnmp/gosnmp"
)

func agentFixture(count int) (*SNMPAgent, *model.Lab, *model.RuntimeView) {
	l := model.Preset(count)
	v := model.Preview(l)
	v.Phase = "running"
	v.Nodes = model.Derive(l, true)
	a := &SNMPAgent{NodeID: "olt-1", Lab: func() model.Lab { return l }, Snapshot: func() model.RuntimeView { return v }, Started: time.Now()}
	return a, &l, &v
}
func packet(t *testing.T, a *SNMPAgent, typ gosnmp.PDUType, oids ...string) *gosnmp.SnmpPacket {
	t.Helper()
	variables := []gosnmp.SnmpPDU{}
	for _, oid := range oids {
		variables = append(variables, gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Null})
	}
	p := gosnmp.SnmpPacket{Version: gosnmp.Version2c, Community: "lab-read", PDUType: typ, RequestID: 37, MaxRepetitions: 16, Variables: variables}
	raw, err := p.MarshalMsg()
	if err != nil {
		t.Fatal(err)
	}
	b := a.Handle(raw)
	decoder := gosnmp.GoSNMP{Logger: gosnmp.NewLogger(log.New(io.Discard, "", 0))}
	reply, err := decoder.SnmpDecodePacket(b)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}
func TestSNMPStandardsAndNoFabricatedVendorOIDs(t *testing.T) {
	a, _, _ := agentFixture(8)
	reply := packet(t, a, gosnmp.GetRequest, ".1.3.6.1.2.1.1.5.0", ".1.3.6.1.4.1.99999.1.0")
	if reply.RequestID != 37 || reply.PDUType != gosnmp.GetResponse || string(reply.Variables[0].Value.([]byte)) != "HSGQ · access" || reply.Variables[1].Type != gosnmp.NoSuchObject {
		t.Fatalf("unexpected SNMP response: %+v", reply)
	}
	reply = packet(t, a, gosnmp.SetRequest, ".1.3.6.1.2.1.1.5.0")
	if reply.Error != gosnmp.NotWritable {
		t.Fatal("unimplemented SNMP SET returned success")
	}
}
func TestSNMPBulkOrderingAndEndOfMIB(t *testing.T) {
	a, _, _ := agentFixture(32)
	reply := packet(t, a, gosnmp.GetBulkRequest, LabOID+".1.1.1")
	if len(reply.Variables) != 16 {
		t.Fatalf("expected 16 repetitions, got %d", len(reply.Variables))
	}
	for i := 1; i < len(reply.Variables); i++ {
		if !OIDLess(reply.Variables[i-1].Name, reply.Variables[i].Name) {
			t.Fatal("GETBULK not in numeric lexicographic OID order")
		}
	}
	reply = packet(t, a, gosnmp.GetNextRequest, ".2.999.9")
	if reply.Variables[0].Type != gosnmp.EndOfMibView {
		t.Fatal("GETNEXT past end has wrong exception")
	}
}
func TestSNMPPowerFailureDoesNotRespond(t *testing.T) {
	a, _, v := agentFixture(8)
	v.Nodes["olt-1"] = model.NodeState{Status: "offline"}
	p := gosnmp.SnmpPacket{Version: gosnmp.Version2c, Community: "lab-read", PDUType: gosnmp.GetRequest, Variables: []gosnmp.SnmpPDU{{Name: ".1.3.6.1.2.1.1.5.0", Type: gosnmp.Null}}}
	b, _ := p.MarshalMsg()
	if a.Handle(b) != nil {
		t.Fatal("powered-off OLT answered SNMP")
	}
}
func TestSNMPUDPWalk(t *testing.T) {
	a, _, _ := agentFixture(100)
	if err := a.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	host, port, _ := net.SplitHostPort(a.conn.LocalAddr().String())
	n, _ := strconv.Atoi(port)
	client := gosnmp.GoSNMP{Target: host, Port: uint16(n), Community: "lab-read", Version: gosnmp.Version2c, Timeout: time.Second, Retries: 0, MaxRepetitions: 25}
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Conn.Close()
	rows, err := client.BulkWalkAll(LabOID + ".1.1.2")
	if err != nil || len(rows) != 100 {
		t.Fatalf("real UDP walk wanted 100 ONU serials, got %d: %v", len(rows), err)
	}
}
