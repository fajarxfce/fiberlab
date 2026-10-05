//go:build ignore

// Run with: go run scripts/probe_native.go --lab-id LAB_ID
// Read-only verification of a running, single-OLT preset via native protocols.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ftthlab/internal/model"
	"github.com/gosnmp/gosnmp"
	"golang.org/x/crypto/ssh"
)

type device struct {
	ID, Kind, IP, Username, Password string
	Services                         map[string]struct {
		Port      int
		Community string
	}
}

func get(client *http.Client, url string, target any) error {
	response, err := client.Get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("control API: HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(response.Body).Decode(target)
}

func run() error {
	base := flag.String("url", "http://127.0.0.1:8787", "Local control API origin")
	id := flag.String("lab-id", "", "Running single-OLT lab to inspect")
	output := flag.String("output", "artifacts/native-management.json", "JSON report")
	flag.Parse()
	if *id == "" {
		return fmt.Errorf("--lab-id is required")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	prefix := strings.TrimRight(*base, "/") + "/api/v1/labs/" + *id
	var lab model.Lab
	if err := get(client, prefix, &lab); err != nil {
		return err
	}
	var connections struct{ Devices []device }
	if err := get(client, prefix+"/connections", &connections); err != nil {
		return err
	}
	var olt device
	count := 0
	for _, d := range connections.Devices {
		if d.Kind == "olt" {
			olt = d
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("probe expects a single-OLT preset")
	}
	expected := make(map[string]string)
	for _, sub := range lab.Subscribers {
		expected[sub.ONUID] = sub.Address
	}

	snmp := &gosnmp.GoSNMP{
		Target: olt.IP, Port: uint16(olt.Services["snmp"].Port),
		Community: olt.Services["snmp"].Community, Version: gosnmp.Version2c,
		Timeout: 2 * time.Second, Retries: 1, MaxRepetitions: 32,
	}
	if err := snmp.Connect(); err != nil {
		return err
	}
	defer snmp.Conn.Close()
	walkStarted := time.Now()
	const baseOID = ".1.3.6.1.4.1.32473.42.1.1"
	identifiers, err := snmp.BulkWalkAll(baseOID + ".1")
	if err != nil {
		return err
	}
	addresses, err := snmp.BulkWalkAll(baseOID + ".9")
	if err != nil {
		return err
	}
	if len(identifiers) != len(expected) || len(addresses) != len(expected) {
		return fmt.Errorf("native SNMP table sizes: IDs %d, IPs %d, expected %d", len(identifiers), len(addresses), len(expected))
	}
	rows := make(map[string]string)
	nativeIDs := make(map[string]bool)
	for _, pdu := range identifiers {
		value, ok := pdu.Value.([]byte)
		if !ok {
			return fmt.Errorf("ONU ID has unexpected SNMP type")
		}
		onu := string(value)
		if expected[onu] == "" || nativeIDs[onu] {
			return fmt.Errorf("unexpected or duplicate native SNMP ONU ID: %s", onu)
		}
		nativeIDs[onu] = true
		rows[pdu.Name[strings.LastIndex(pdu.Name, ".")+1:]] = onu
	}
	for _, pdu := range addresses {
		value, ok := pdu.Value.([]byte)
		if !ok {
			return fmt.Errorf("ONU IP has unexpected SNMP type")
		}
		onu := rows[pdu.Name[strings.LastIndex(pdu.Name, ".")+1:]]
		if expected[onu] == "" || string(value) != expected[onu] {
			return fmt.Errorf("native SNMP assigned IP mismatch for %s: %q", onu, value)
		}
	}
	walkMS := time.Since(walkStarted).Milliseconds()

	address := net.JoinHostPort(olt.IP, fmt.Sprint(olt.Services["ssh"].Port))
	connection, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		return err
	}
	defer connection.Close()
	if err = connection.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return err
	}
	// The disposable lab endpoint comes from the local control API.
	sshConn, channels, requests, err := ssh.NewClientConn(connection, address, &ssh.ClientConfig{
		User: olt.Username, Auth: []ssh.AuthMethod{ssh.Password(olt.Password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		return err
	}
	native := ssh.NewClient(sshConn, channels, requests)
	defer native.Close()
	exec := func(command string) (string, error) {
		session, err := native.NewSession()
		if err != nil {
			return "", err
		}
		defer session.Close()
		data, err := session.CombinedOutput(command)
		return string(data), err
	}
	system, err := exec("lab system")
	if err != nil {
		return err
	}
	list, err := exec("lab onu list")
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, line := range strings.Split(list, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && expected[fields[0]] != "" {
			seen[fields[0]] = true
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("native SSH lists %d/%d ONUs", len(seen), len(expected))
	}
	unsupported, err := exec("display onu information")
	exit, ok := err.(*ssh.ExitError)
	if !ok || exit.ExitStatus() != 1 || !strings.Contains(unsupported, "UNSUPPORTED") {
		return fmt.Errorf("unverified vendor command did not report UNSUPPORTED")
	}
	report := map[string]any{
		"status": "passed", "checkedAt": time.Now().UTC(), "labId": lab.ID,
		"managementIP": olt.IP, "snmpONURows": len(rows), "snmpAssignedIPs": len(addresses),
		"snmpWalkMilliseconds": walkMS, "sshONURows": len(seen),
		"unsupportedVendorCommandRejected": true, "sshSystem": system, "sshONUList": list,
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(*output), 0700); err != nil {
		return err
	}
	if err = os.WriteFile(*output, append(data, '\n'), 0600); err != nil {
		return err
	}
	fmt.Printf("Native SNMP: %d ONU rows and assigned IPs (%d ms); SSH: %d ONUs; vendor fallback: UNSUPPORTED\nReport: %s\n", len(rows), walkMS, len(seen), *output)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
