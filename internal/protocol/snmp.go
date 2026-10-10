package protocol

import (
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ftthlab/internal/model"
	"github.com/gosnmp/gosnmp"
)

// LabOID is an explicitly documented, experimental reference MIB. It is NOT
// an HSGQ enterprise OID. The separate HSGQ subset is defined in hsgq.go.
const LabOID = ".1.3.6.1.4.1.32473.42"

type SNMPAgent struct {
	NodeID   string
	Lab      func() model.Lab
	Snapshot func() model.RuntimeView
	Started  time.Time
	conn     net.PacketConn
	done     chan struct{}
	mu       sync.Mutex
	table    []gosnmp.SnmpPDU
	tableAt  time.Time
}

func (a *SNMPAgent) Listen(address string) error {
	c, err := net.ListenPacket("udp4", address)
	if err != nil {
		return err
	}
	a.conn = c
	a.done = make(chan struct{})
	if a.Started.IsZero() {
		a.Started = time.Now()
	}
	go a.serve()
	return nil
}
func (a *SNMPAgent) Close() {
	if a.conn != nil {
		_ = a.conn.Close()
		<-a.done
	}
}
func (a *SNMPAgent) serve() {
	defer close(a.done)
	buf := make([]byte, 65535)
	for {
		n, peer, err := a.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		if response := a.Handle(buf[:n]); response != nil {
			_, _ = a.conn.WriteTo(response, peer)
		}
	}
}
func (a *SNMPAgent) Handle(raw []byte) []byte {
	decoder := &gosnmp.GoSNMP{Logger: gosnmp.NewLogger(log.New(io.Discard, "", 0))}
	request, err := decoder.SnmpDecodePacket(raw)
	if err != nil || request.Version != gosnmp.Version2c || len(request.Variables) > 64 {
		return nil
	}
	l := a.Lab()
	node, ok := l.Node(a.NodeID)
	if !ok || subtle.ConstantTimeCompare([]byte(request.Community), []byte(node.Config.Community)) != 1 {
		return nil
	}
	view := a.Snapshot()
	state := view.Nodes[a.NodeID]
	if state.Status != "online" {
		return nil
	}
	response := &gosnmp.SnmpPacket{Version: request.Version, Community: request.Community, PDUType: gosnmp.GetResponse, RequestID: request.RequestID, Variables: []gosnmp.SnmpPDU{}}
	table := a.cachedTable(l, view)
	next := func(oid string) gosnmp.SnmpPDU {
		i := sort.Search(len(table), func(i int) bool { return OIDLess(oid, table[i].Name) })
		if i == len(table) {
			return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.EndOfMibView}
		}
		return table[i]
	}
	get := func(oid string) gosnmp.SnmpPDU {
		for _, p := range table {
			if trimOID(p.Name) == trimOID(oid) {
				return p
			}
		}
		return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.NoSuchObject}
	}
	switch request.PDUType {
	case gosnmp.GetRequest:
		for _, v := range request.Variables {
			response.Variables = append(response.Variables, get(v.Name))
		}
	case gosnmp.GetNextRequest:
		for _, v := range request.Variables {
			response.Variables = append(response.Variables, next(v.Name))
		}
	case gosnmp.GetBulkRequest:
		nr := min(int(request.NonRepeaters), len(request.Variables))
		for _, v := range request.Variables[:nr] {
			response.Variables = append(response.Variables, next(v.Name))
		}
		last := append([]gosnmp.SnmpPDU(nil), request.Variables[nr:]...)
		// Truncate at a whole row. SNMP4J TableUtils assigns columns by their
		// position in each repetition; a partial last row breaks multi-column
		// inventory/optical joins. Exhausted columns retain EndOfMibView slots.
		repetitions := min(int(request.MaxRepetitions), 32)
		if len(last) > 0 {
			repetitions = min(repetitions, (256-nr)/len(last))
		}
		for r := 0; r < repetitions; r++ {
			for i, v := range last {
				last[i] = next(v.Name)
				response.Variables = append(response.Variables, last[i])
			}
		}
	case gosnmp.SetRequest:
		response.Error = gosnmp.NotWritable
		response.ErrorIndex = 1
		response.Variables = request.Variables
	default:
		return nil
	}
	b, err := response.MarshalMsg()
	if err != nil || len(b) > 60000 {
		return nil
	}
	return b
}
func (a *SNMPAgent) cachedTable(l model.Lab, v model.RuntimeView) []gosnmp.SnmpPDU {
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Since(a.tableAt) < 500*time.Millisecond {
		return a.table
	}
	a.table = BuildMIB(l, v, a.NodeID, time.Since(a.Started))
	a.tableAt = time.Now()
	return a.table
}
func BuildMIB(l model.Lab, v model.RuntimeView, oltID string, uptime time.Duration) []gosnmp.SnmpPDU {
	table := []gosnmp.SnmpPDU{}
	add := func(oid string, typ gosnmp.Asn1BER, value any) {
		table = append(table, gosnmp.SnmpPDU{Name: oid, Type: typ, Value: value})
	}
	node, _ := l.Node(oltID)
	add(".1.3.6.1.2.1.1.1.0", gosnmp.OctetString, "Fiberlab HSGQ compatibility; G01ID GPON + E04I EPON telemetry; 8 virtual PON ports")
	add(".1.3.6.1.2.1.1.2.0", gosnmp.ObjectIdentifier, LabOID)
	add(".1.3.6.1.2.1.1.3.0", gosnmp.TimeTicks, uint32(uptime/(10*time.Millisecond)))
	add(".1.3.6.1.2.1.1.4.0", gosnmp.OctetString, "local lab operator")
	add(".1.3.6.1.2.1.1.5.0", gosnmp.OctetString, node.Label)
	add(".1.3.6.1.2.1.1.6.0", gosnmp.OctetString, l.Name)
	add(".1.3.6.1.2.1.1.7.0", gosnmp.Integer, 2)
	add(".1.3.6.1.2.1.2.1.0", gosnmp.Integer, 9)
	sessions := map[string]model.Session{}
	for _, s := range v.Sessions {
		sessions[s.ONUID] = s
	}
	for index := 1; index <= 9; index++ {
		name := "uplink"
		port := "uplink"
		oper := 1
		admin := 1
		var rx, tx uint64
		if index > 1 {
			name = fmt.Sprintf("PON %02d", index-1)
			port = fmt.Sprintf("pon%d", index-1)
		}
		if index == 1 && !v.Nodes[oltID].ServicePathUp {
			oper = 2
		}
		for _, f := range l.Faults {
			if f.Kind == "pon_down" && f.TargetID == oltID+":"+port {
				oper = 2
				admin = 2
			}
		}
		for _, s := range v.Sessions {
			state := v.Nodes[s.ONUID]
			if state.OLTID == oltID && (index == 1 || state.PON == port) {
				rx += s.TXBytes
				tx += s.RXBytes
			}
		}
		base := ".1.3.6.1.2.1.2.2.1."
		suffix := fmt.Sprintf(".%d", index)
		add(base+"1"+suffix, gosnmp.Integer, index)
		add(base+"2"+suffix, gosnmp.OctetString, name)
		add(base+"3"+suffix, gosnmp.Integer, 6)
		add(base+"4"+suffix, gosnmp.Integer, 1500)
		add(base+"5"+suffix, gosnmp.Gauge32, uint32(1000000000))
		add(base+"6"+suffix, gosnmp.OctetString, []byte{2, 70, 84, 0, 0, byte(index)})
		add(base+"7"+suffix, gosnmp.Integer, admin)
		add(base+"8"+suffix, gosnmp.Integer, oper)
		add(base+"9"+suffix, gosnmp.TimeTicks, uint32(0))
		add(base+"10"+suffix, gosnmp.Counter32, uint32(rx))
		add(base+"16"+suffix, gosnmp.Counter32, uint32(tx))
		add(".1.3.6.1.2.1.31.1.1.1.1"+suffix, gosnmp.OctetString, name)
		add(".1.3.6.1.2.1.31.1.1.1.6"+suffix, gosnmp.Counter64, rx)
		add(".1.3.6.1.2.1.31.1.1.1.10"+suffix, gosnmp.Counter64, tx)
	}
	row := 0
	for _, onu := range l.SortedNodes("onu") {
		state := v.Nodes[onu.ID]
		if state.OLTID != oltID {
			continue
		}
		row++
		suffix := fmt.Sprintf(".%d", row)
		status := 2
		switch state.Status {
		case "online":
			status = 1
		case "unregistered":
			status = 3
		case "offline":
			status = 4
		}
		pon, _ := strconv.Atoi(strings.TrimPrefix(state.PON, "pon"))
		rx := -9999
		if state.RXDBm != nil {
			rx = int(*state.RXDBm * 100)
		}
		s := sessions[onu.ID]
		base := LabOID + ".1.1."
		add(base+"1"+suffix, gosnmp.OctetString, onu.ID)
		add(base+"2"+suffix, gosnmp.OctetString, onu.Config.Serial)
		add(base+"3"+suffix, gosnmp.Integer, status)
		add(base+"4"+suffix, gosnmp.Integer, rx)
		add(base+"5"+suffix, gosnmp.Integer, pon)
		add(base+"6"+suffix, gosnmp.Integer, onu.Config.VLAN)
		add(base+"7"+suffix, gosnmp.Counter64, s.RXBytes)
		add(base+"8"+suffix, gosnmp.Counter64, s.TXBytes)
		add(base+"9"+suffix, gosnmp.OctetString, s.Address)
		add(base+"10"+suffix, gosnmp.OctetString, s.Username)
	}
	table = append(table, hsgqMIB(l, v, oltID)...)
	sort.Slice(table, func(i, j int) bool { return OIDLess(table[i].Name, table[j].Name) })
	return table
}
func trimOID(s string) string { return strings.Trim(s, ".") }
func OIDLess(a, b string) bool {
	aa := strings.Split(trimOID(a), ".")
	bb := strings.Split(trimOID(b), ".")
	for i := 0; i < len(aa) && i < len(bb); i++ {
		x, _ := strconv.ParseUint(aa[i], 10, 64)
		y, _ := strconv.ParseUint(bb[i], 10, 64)
		if x != y {
			return x < y
		}
	}
	return len(aa) < len(bb)
}
func SendONUTrap(sourceIP, address, community, onuID, status string, uptime time.Duration) error {
	packet := gosnmp.SnmpPacket{Version: gosnmp.Version2c, Community: community, PDUType: gosnmp.SNMPv2Trap, RequestID: uint32(time.Now().UnixNano()), Variables: []gosnmp.SnmpPDU{{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(uptime / (10 * time.Millisecond))}, {Name: ".1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: LabOID + ".0.1"}, {Name: LabOID + ".2.1.0", Type: gosnmp.OctetString, Value: onuID}, {Name: LabOID + ".2.2.0", Type: gosnmp.OctetString, Value: status}}}
	b, err := packet.MarshalMsg()
	if err != nil {
		return err
	}
	remote, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return err
	}
	conn, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP(sourceIP)}, remote)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, err = conn.Write(b)
	return err
}
