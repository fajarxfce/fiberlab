package protocol

import (
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"

	"ftthlab/internal/model"
	"github.com/gosnmp/gosnmp"
)

const (
	HSGQProfile       = "HSGQ · GPON / EPON SNMP"
	HSGQGPONInventory = ".1.3.6.1.4.1.50224.3.12.2.1"
	HSGQGPONOptical   = ".1.3.6.1.4.1.50224.3.12.3.1"
	HSGQEPONInventory = ".1.3.6.1.4.1.50224.3.3.2.1"
	HSGQEPONOptical   = ".1.3.6.1.4.1.50224.3.3.3.1"
)

// HSGQONU identifies the same simulated ONU in both wire formats. The FTTH
// HsgqEponSnmpAdapter uses the normalized MAC as its customer serialNumber.
type HSGQONU struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	OLTID        string `json:"oltId"`
	PON          int    `json:"pon"`
	Number       int    `json:"onuNumber"`
	Index        uint32 `json:"snmpIndex"`
	GPONIndex    uint32 `json:"gponIndex"`
	Serial       string `json:"serial"`
	MAC          string `json:"mac"`
	FTTHIdentity string `json:"ftthIdentity"`
}

// HSGQONUs assigns 0x0100PPNN indices within the physical OLT/PON, including
// offline ONUs. Power, fiber, authorization and billing faults must not change
// row identities. Topology edits can reassign NN; MAC/serial remain the identity.
// Model validation limits each PON to 128 ONUs, so NN always fits in one byte.
func HSGQONUs(l model.Lab, v model.RuntimeView, oltID string) []HSGQONU {
	rows := []HSGQONU{}
	numbers := map[int]int{}
	for _, n := range l.SortedNodes("onu") {
		state := v.Nodes[n.ID]
		if state.OLTID != oltID {
			continue
		}
		pon, err := strconv.Atoi(strings.TrimPrefix(state.PON, "pon"))
		if err != nil || pon < 1 || pon > 8 {
			continue
		}
		numbers[pon]++
		mac := model.ONUMAC(n)
		rows = append(rows, HSGQONU{
			ID: n.ID, Name: n.Label, OLTID: oltID, PON: pon, Number: numbers[pon],
			Index:     uint32(0x01000000 | pon<<8 | numbers[pon]),
			GPONIndex: uint32(0x01000000 | pon<<8 | (numbers[pon] - 1)),
			Serial:    n.Config.Serial, MAC: mac,
			FTTHIdentity: strings.ToUpper(strings.ReplaceAll(mac, ":", "")),
		})
	}
	return rows
}

// hsgqMIB implements only the evidenced inventory/optical columns. See
// testdata/hsgq/README.md for the independent G01ID capture and E04I adapter
// fixtures. Serving both tables is a Fiberlab compatibility extension: the real
// G01ID does not expose the EPON table. Unknown vendor OIDs remain unsupported.
func hsgqMIB(l model.Lab, v model.RuntimeView, oltID string) []gosnmp.SnmpPDU {
	table := []gosnmp.SnmpPDU{}
	add := func(oid string, typ gosnmp.Asn1BER, value any) {
		table = append(table, gosnmp.SnmpPDU{Name: oid, Type: typ, Value: value})
	}
	nodes := map[string]model.Node{}
	for _, n := range l.Nodes {
		nodes[n.ID] = n
	}
	for _, row := range HSGQONUs(l, v, oltID) {
		n := nodes[row.ID]
		state := v.Nodes[row.ID]
		suffix := fmt.Sprintf(".%d", row.Index)
		gponSuffix := fmt.Sprintf(".%d", row.GPONIndex)
		status := 2
		if state.OpticalUp && n.Config.Registered && state.Status == "online" {
			status = 1
		}
		add(HSGQGPONInventory+".2"+gponSuffix, gosnmp.OctetString, fmt.Sprintf("ONT%02d/%03d", row.PON, row.Number-1))
		add(HSGQGPONInventory+".4"+gponSuffix, gosnmp.Integer, status)
		// G01ID returns a 12-character ASCII serial, not eight binary octets.
		add(HSGQGPONInventory+".15"+gponSuffix, gosnmp.OctetString, row.Serial)
		add(HSGQEPONInventory+".2"+suffix, gosnmp.OctetString, fmt.Sprintf("ONU%02d/%02d", row.PON, row.Number))
		mac, _ := net.ParseMAC(row.MAC) // validated by model.Validate
		add(HSGQEPONInventory+".7"+suffix, gosnmp.OctetString, []byte(mac))
		add(HSGQEPONInventory+".8"+suffix, gosnmp.Integer, status)
		// Inventory persists through faults. Offline/unregistered ONUs have no
		// optical row; a missing signal must not become 0 dBm or stale telemetry.
		if status == 1 && state.RXDBm != nil {
			for _, optical := range []struct{ base, suffix string }{{HSGQGPONOptical, gponSuffix}, {HSGQEPONOptical, suffix}} {
				add(optical.base+".4"+optical.suffix+".0.0", gosnmp.Integer, int(math.Round(*state.RXDBm*100)))
				add(optical.base+".5"+optical.suffix+".0.0", gosnmp.Integer, int(math.Round(n.Config.TxDBm*100)))
			}
		}
	}
	return table
}
