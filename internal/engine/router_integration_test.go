package engine

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ftthlab/internal/model"
	"ftthlab/internal/protocol"
	"github.com/go-routeros/routeros/v3"
	"layeh.com/radius"
	"layeh.com/radius/rfc2865"
	"layeh.com/radius/rfc2866"
)

// Opt in with FIBERLAB_CHR_IMAGE=/absolute/path/to/chr.img. This boots the
// genuine image with KVM and user networking; it needs no host network changes.
func TestCHRBootAndNativeAPI(t *testing.T) {
	base := os.Getenv("FIBERLAB_CHR_IMAGE")
	if base == "" {
		t.Skip("set FIBERLAB_CHR_IMAGE to exercise a real MikroTik CHR VM")
	}
	if !filepath.IsAbs(base) {
		t.Fatal("FIBERLAB_CHR_IMAGE must be absolute")
	}
	dir := t.TempDir()
	disk := filepath.Join(dir, "chr.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", "-F", "raw", "-b", base, disk).CombinedOutput(); err != nil {
		t.Fatalf("create overlay: %v: %s", err, out)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	winboxListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	winboxPort := winboxListener.Addr().(*net.TCPAddr).Port
	winboxListener.Close()
	lab := model.Preset(8)
	lab.Radius.InterimSeconds = 10
	freeUDPPort := func() int {
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := conn.LocalAddr().(*net.UDPAddr).Port
		conn.Close()
		return port
	}
	authPort, acctPort, disconnectPort := freeUDPPort(), freeUDPPort(), freeUDPPort()
	var blocked atomic.Bool
	accounting := make(chan protocol.Accounting, 128)
	radiusServer := &protocol.RadiusService{AllowedNAS: map[string]bool{"127.0.0.1": true}, Lab: func() model.Lab {
		copy := lab
		copy.Subscribers = append([]model.Subscriber{}, lab.Subscribers...)
		copy.Subscribers[0].Enabled = !blocked.Load()
		return copy
	}, OnAccounting: func(a protocol.Accounting) { accounting <- a }}
	if err = radiusServer.Listen(fmt.Sprintf("127.0.0.1:%d", authPort), fmt.Sprintf("127.0.0.1:%d", acctPort)); err != nil {
		t.Fatal(err)
	}
	defer radiusServer.Close()
	node := lab.SortedNodes("router")[0]
	serial := filepath.Join(dir, "serial.sock")
	qmp := filepath.Join(dir, "qmp.sock")
	args := []string{"-machine", "q35,accel=kvm", "-cpu", "host", "-m", "1024", "-smp", "1", "-nodefaults", "-display", "none", "-drive", "file=" + disk + ",if=virtio,format=qcow2", "-serial", "unix:" + serial + ",server=on,wait=off", "-qmp", "unix:" + qmp + ",server=on,wait=off"}
	for i := 0; i < 6; i++ {
		backend := fmt.Sprintf("user,id=n%d", i)
		if i == 0 {
			backend += fmt.Sprintf(",hostfwd=tcp:127.0.0.1:%d-:8728,hostfwd=tcp:127.0.0.1:%d-:8291,hostfwd=udp:127.0.0.1:%d-:3799", port, winboxPort, disconnectPort)
		}
		if i == 2 || i == 3 {
			backend = fmt.Sprintf("hubport,id=n%d,hubid=1", i)
		}
		args = append(args, "-netdev", backend, "-device", fmt.Sprintf("virtio-net-pci,netdev=n%d,mac=02:46:54:01:00:%02x", i, i+1))
	}
	logFile, err := os.Create(filepath.Join(dir, "qemu.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	vm := exec.Command("qemu-system-x86_64", args...)
	vm.Stdout, vm.Stderr = logFile, logFile
	if err = vm.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vm.Process.Kill(); _ = vm.Wait() })
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var trace strings.Builder
	if err = bootstrapSerial(ctx, serial, node.Config.Username, node.Config.Password, "10.0.2.15", &trace); err != nil {
		t.Fatalf("bootstrap: %v; %s; transcript: %q", err, readTail(logFile.Name(), 4096), trace.String())
	}
	client, err := DialRouter(ctx, fmt.Sprintf("127.0.0.1:%d", port), node.Config.Username, node.Config.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for i := 0; i < 2; i++ {
		if err = ConfigureRouter(ctx, client, lab, node, 0); err != nil {
			t.Fatalf("configure pass %d: %v", i+1, err)
		}
	}
	res, err := client.RunArgsContext(ctx, []string{"/system/resource/print"})
	if err != nil || len(res.Re) != 1 || !strings.Contains(res.Re[0].Map["board-name"], "CHR") {
		t.Fatalf("genuine CHR resource query: %+v %v", res, err)
	}
	t.Logf("Verified genuine RouterOS %s (%s)", res.Re[0].Map["version"], res.Re[0].Map["board-name"])
	winboxID := ""
	verifyWinbox := func(c *routeros.Client) {
		t.Helper()
		services, err := c.RunArgsContext(ctx, []string{"/ip/service/print", "?name=winbox"})
		if err != nil || len(services.Re) != 1 || services.Re[0].Map["disabled"] != "false" || services.Re[0].Map["port"] != "8291" {
			t.Fatalf("Winbox service: %+v %v", services, err)
		}
		winboxID = services.Re[0].Map[".id"]
		groups, err := c.RunArgsContext(ctx, []string{"/user/group/print", "?name=full"})
		if err != nil || len(groups.Re) != 1 || !strings.Contains(groups.Re[0].Map["policy"], "winbox") {
			t.Fatalf("admin group cannot use Winbox: %+v %v", groups, err)
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", winboxPort), 3*time.Second)
		if err != nil {
			t.Fatalf("Winbox TCP endpoint: %v", err)
		}
		conn.Close()
	}
	verifyWinbox(client)
	verifyCHRExternalRadiusRouting(t, ctx, client, lab, node)
	servers, err := client.RunArgsContext(ctx, []string{"/interface/pppoe-server/server/print", "?comment=fiberlab"})
	if err != nil || len(servers.Re) != 4*len(node.Config.ServiceVLANs) {
		t.Fatalf("PPPoE servers after reconcile: %+v %v", servers, err)
	}
	verifyCHRRadius(t, ctx, client, lab, authPort, acctPort, disconnectPort, &blocked, accounting)
	verifyCHRExternalRadiusPool(t, ctx, client, lab, node)
	if err = QMP(ctx, qmp, "stop"); err != nil {
		t.Fatal(err)
	}
	if err = QMP(ctx, qmp, "cont"); err != nil {
		t.Fatal(err)
	}
	// A persisted overlay must accept the saved password after a guest reboot.
	if _, err = client.RunArgsContext(ctx, []string{"/ip/service/set", "=.id=" + winboxID, "=disabled=yes", "=port=18300"}); err != nil {
		t.Fatal(err)
	}
	client.Close()
	if err = QMP(ctx, qmp, "system_reset"); err != nil {
		t.Fatal(err)
	}
	rebootCtx, rebootCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer rebootCancel()
	if err = BootstrapSerial(rebootCtx, serial, node.Config.Username, node.Config.Password, "10.0.2.15"); err != nil {
		t.Fatalf("reprovision persistent overlay: %v", err)
	}
	reconnected, err := DialRouter(rebootCtx, fmt.Sprintf("127.0.0.1:%d", port), node.Config.Username, node.Config.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close()
	verifyWinbox(reconnected)
	t.Log("Verified saved router login and Winbox TCP 8291 before and after reboot")
}

func verifyCHRExternalRadiusRouting(t *testing.T, ctx context.Context, client *routeros.Client, lab model.Lab, node model.Node) {
	t.Helper()
	run := func(args ...string) *routeros.Reply {
		t.Helper()
		reply, err := client.RunArgsContext(ctx, args)
		if err != nil {
			t.Fatalf("CHR %s: %v", args[0], err)
		}
		return reply
	}
	const address = "198.51.100.42"
	const comment = "fiberlab-internet-regression"
	run("/ip/route/add", "=dst-address=0.0.0.0/0", "=gateway=198.18.0.1", "=comment="+comment)
	// Reproduce the old managed route left behind when RADIUS and the VPN
	// endpoint share an IP. Reconciliation must remove it and use the WAN.
	run("/ip/route/add", "=dst-address="+address+"/32", "=gateway=10.203.0.1", "=comment=fiberlab")
	external := lab
	external.Radius.Mode, external.Radius.Address = "external", address
	for pass := 0; pass < 2; pass++ {
		if err := ConfigureRouter(ctx, client, external, node, 0); err != nil {
			t.Fatal(err)
		}
		if rows := run("/ip/route/print", "?dst-address="+address+"/32").Re; len(rows) != 0 {
			t.Fatalf("external RADIUS overrides the existing WAN route on pass %d: %+v", pass+1, rows[0].Map)
		}
		if rows := run("/ip/route/print", "?comment="+comment).Re; len(rows) != 1 || rows[0].Map["gateway"] != "198.18.0.1" {
			t.Fatal("external RADIUS changed the operator's default route")
		}
		rows := run("/radius/print", "?comment=fiberlab", "=.proplist=address").Re
		if len(rows) != 1 || rows[0].Map["address"] != address {
			t.Fatal("external RADIUS destination was not configured")
		}
	}
	for _, row := range run("/ip/route/print", "?comment="+comment).Re {
		run("/ip/route/remove", "=.id="+row.Map[".id"])
	}
	if err := ConfigureRouter(ctx, client, lab, node, 0); err != nil {
		t.Fatal(err)
	}
	t.Log("Verified external RADIUS preserves the WAN route across repeated configuration")
}

// The CHR's second access NIC acts as a real PPPoE client, wired back through
// a QEMU Ethernet hub. This verifies NAS interoperability without root or PPP.
func verifyCHRRadius(t *testing.T, ctx context.Context, client *routeros.Client, lab model.Lab, authPort, acctPort, disconnectPort int, blocked *atomic.Bool, accounting <-chan protocol.Accounting) {
	t.Helper()
	run := func(args ...string) *routeros.Reply {
		t.Helper()
		r, err := client.RunArgsContext(ctx, args)
		if err != nil {
			t.Fatalf("CHR %s: %v", args[0], err)
		}
		return r
	}
	for _, row := range run("/interface/pppoe-server/server/print").Re {
		if row.Map["interface"] == "fl-2-v100" {
			run("/interface/pppoe-server/server/set", "=.id="+row.Map[".id"], "=disabled=yes")
		}
	}
	for _, row := range run("/radius/print", "?comment=fiberlab").Re {
		run("/radius/set", "=.id="+row.Map[".id"], "=address=10.0.2.2", "=authentication-port="+fmt.Sprint(authPort), "=accounting-port="+fmt.Sprint(acctPort), "=src-address=10.0.2.15")
	}
	sub := lab.Subscribers[0]
	run("/interface/pppoe-client/add", "=name=fiberlab-test-client", "=interface=fl-2-v100", "=user="+sub.Username, "=password="+sub.Password, "=allow=pap", "=add-default-route=no", "=use-peer-dns=no", "=disabled=no")
	clientRows := run("/interface/pppoe-client/print", "?name=fiberlab-test-client")
	id := clientRows.Re[0].Map[".id"]
	waitActive := func(count int) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			rows := run("/ppp/active/print", "?name="+sub.Username)
			if len(rows.Re) == count {
				if count > 0 && rows.Re[0].Map["address"] != sub.Address {
					t.Fatalf("RADIUS address was not assigned: %+v", rows.Re[0].Map)
				}
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("CHR PPP active count never reached %d; inspect PPPoE/RADIUS compatibility", count)
	}
	waitAccounting := func(kind string) protocol.Accounting {
		t.Helper()
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		for {
			select {
			case a := <-accounting:
				if a.Username == sub.Username && a.Kind == kind {
					return a
				}
			case <-timer.C:
				t.Fatalf("genuine CHR Accounting-%s was not received", kind)
				return protocol.Accounting{}
			}
		}
	}
	waitActive(1)
	start := waitAccounting("Start")
	if start.SessionID == "" {
		t.Fatal("CHR did not supply an accounting session ID")
	}
	waitAccounting("Interim")
	queueFound := false
	for _, row := range run("/queue/simple/print").Re {
		if strings.Contains(row.Map["name"], sub.Username) && row.Map["max-limit"] == "1000000/1000000" {
			queueFound = true
		}
	}
	if !queueFound {
		t.Fatal("Mikrotik-Rate-Limit did not create the actual subscriber queue")
	}
	run("/interface/pppoe-client/set", "=.id="+id, "=disabled=yes")
	waitActive(0)
	waitAccounting("Stop")
	run("/interface/pppoe-client/set", "=.id="+id, "=allow=chap", "=disabled=no")
	waitActive(1)
	start = waitAccounting("Start")
	blocked.Store(true)
	disconnectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := protocol.Disconnect(disconnectCtx, fmt.Sprintf("127.0.0.1:%d", disconnectPort), lab.Radius.Secret, sub.Username, start.SessionID); err != nil {
		t.Fatalf("real CHR RFC5176 Disconnect: %v", err)
	}
	waitActive(0)
	waitAccounting("Stop")
	run("/interface/pppoe-client/remove", "=.id="+id)
	t.Log("Real CHR PPPoE PAP/CHAP, Framed-IP, rate queue, Start/Interim/Stop and Disconnect-ACK verified")
}

// An external server can authenticate successfully without Framed-IP-Address.
// Exercise actual IPCP and accounting so a missing PPP pool cannot pass merely
// because the NAS received Access-Accept.
func verifyCHRExternalRadiusPool(t *testing.T, ctx context.Context, client *routeros.Client, lab model.Lab, node model.Node) {
	t.Helper()
	sub := lab.Subscribers[0]
	accounting := make(chan protocol.Accounting, 16)
	server := &radius.PacketServer{
		SecretSource: radius.StaticSecretSource([]byte(lab.Radius.Secret)),
		Handler: radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {
			var response *radius.Packet
			switch r.Code {
			case radius.CodeAccessRequest:
				response = r.Response(radius.CodeAccessReject)
				if rfc2865.UserName_GetString(r.Packet) == sub.Username && rfc2865.UserPassword_GetString(r.Packet) == sub.Password {
					response = r.Response(radius.CodeAccessAccept)
					_ = rfc2865.ServiceType_Set(response, rfc2865.ServiceType_Value_FramedUser)
					_ = rfc2865.FramedProtocol_Set(response, rfc2865.FramedProtocol_Value_PPP)
					// Deliberately omit Framed-IP-Address and Framed-Pool.
				}
			case radius.CodeAccountingRequest:
				response = r.Response(radius.CodeAccountingResponse)
				if rfc2865.UserName_GetString(r.Packet) == sub.Username && rfc2866.AcctStatusType_Get(r.Packet) == rfc2866.AcctStatusType_Value_Start {
					select {
					case accounting <- protocol.Accounting{Address: rfc2865.FramedIPAddress_Get(r.Packet).String(), SessionID: rfc2866.AcctSessionID_GetString(r.Packet)}:
					default:
					}
				}
			default:
				return
			}
			if err := protocol.SignMessageAuthenticator(response); err != nil {
				t.Errorf("sign external RADIUS response: %v", err)
				return
			}
			if err := w.Write(response); err != nil {
				t.Errorf("write external RADIUS response: %v", err)
			}
		}),
	}
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(conn) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			t.Errorf("stop external RADIUS server: %v", err)
		}
		_ = conn.Close()
		select {
		case err := <-done:
			if err != nil && err != radius.ErrServerShutdown {
				t.Errorf("external RADIUS server: %v", err)
			}
		case <-shutdownCtx.Done():
			t.Error("external RADIUS server did not stop")
		}
	}()
	run := func(args ...string) *routeros.Reply {
		t.Helper()
		reply, err := client.RunArgsContext(ctx, args)
		if err != nil {
			t.Fatalf("CHR %s: %v", args[0], err)
		}
		return reply
	}
	run("/ip/pool/add", "=name=operator-pool", "=ranges=10.20.0.2-10.20.0.254")
	external := lab
	external.Radius.Mode, external.Radius.Address = "external", "10.0.2.2"
	external.Radius.AuthPort = conn.LocalAddr().(*net.UDPAddr).Port
	external.Radius.AccountingPort = external.Radius.AuthPort
	for pass := 0; pass < 2; pass++ {
		if err := ConfigureRouter(ctx, client, external, node, 0); err != nil {
			t.Fatalf("configure external RADIUS pass %d: %v", pass+1, err)
		}
	}
	if rows := run("/ip/pool/print", "?name=operator-pool").Re; len(rows) != 1 || rows[0].Map["ranges"] != "10.20.0.2-10.20.0.254" {
		t.Fatal("reconciliation changed an operator-created pool")
	}
	for _, row := range run("/radius/print", "?comment=fiberlab", "=.proplist=.id").Re {
		run("/radius/set", "=.id="+row.Map[".id"], "=src-address=10.0.2.15")
	}
	for _, row := range run("/interface/pppoe-server/server/print", "?interface=fl-2-v100").Re {
		run("/interface/pppoe-server/server/set", "=.id="+row.Map[".id"], "=disabled=yes")
	}
	run("/interface/pppoe-client/add", "=name=fiberlab-pool-test-client", "=interface=fl-2-v100", "=user="+sub.Username, "=password="+sub.Password, "=allow=pap", "=add-default-route=no", "=use-peer-dns=no", "=disabled=no")
	defer func() {
		for _, row := range run("/interface/pppoe-client/print", "?name=fiberlab-pool-test-client").Re {
			run("/interface/pppoe-client/remove", "=.id="+row.Map[".id"])
		}
	}()
	assigned := ""
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		rows := run("/ppp/active/print", "?name="+sub.Username).Re
		peer := run("/interface/pppoe-client/print", "?name=fiberlab-pool-test-client").Re
		if len(rows) == 1 && len(peer) == 1 && peer[0].Map["running"] == "true" {
			assigned = rows[0].Map["address"]
			if rows[0].Map["radius"] != "true" {
				t.Fatal("PPP subscriber did not authenticate using external RADIUS")
			}
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	address, err := netip.ParseAddr(assigned)
	if err != nil || address.Compare(netip.MustParseAddr("172.30.0.10")) < 0 || address.Compare(netip.MustParseAddr("172.30.3.254")) > 0 {
		t.Fatalf("external RADIUS without Framed-IP did not establish PPP with a pool address: %q", assigned)
	}
	select {
	case start := <-accounting:
		if start.Address != assigned || start.SessionID == "" {
			t.Fatalf("external Accounting-Start did not report the assigned pool address: %+v", start)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("external RADIUS did not receive Accounting-Start for the pool session")
	}
	t.Log("Verified external Access-Accept without Framed-IP establishes PPP and reports the pool IP in accounting")
}
