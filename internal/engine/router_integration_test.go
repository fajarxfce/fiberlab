package engine

import (
	"context"
	"fmt"
	"net"
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
			backend += fmt.Sprintf(",hostfwd=tcp:127.0.0.1:%d-:8728,hostfwd=udp:127.0.0.1:%d-:3799", port, disconnectPort)
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
	servers, err := client.RunArgsContext(ctx, []string{"/interface/pppoe-server/server/print", "?comment=fiberlab"})
	if err != nil || len(servers.Re) != 4*len(node.Config.ServiceVLANs) {
		t.Fatalf("PPPoE servers after reconcile: %+v %v", servers, err)
	}
	verifyCHRRadius(t, ctx, client, lab, authPort, acctPort, disconnectPort, &blocked, accounting)
	if err = QMP(ctx, qmp, "stop"); err != nil {
		t.Fatal(err)
	}
	if err = QMP(ctx, qmp, "cont"); err != nil {
		t.Fatal(err)
	}
	// A persisted overlay must accept the saved password after a guest reboot.
	client.Close()
	if err = QMP(ctx, qmp, "system_reset"); err != nil {
		t.Fatal(err)
	}
	rebootCtx, rebootCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer rebootCancel()
	if err = BootstrapSerial(rebootCtx, serial, node.Config.Username, node.Config.Password, "10.0.2.15"); err != nil {
		t.Fatalf("reprovision persistent overlay: %v", err)
	}
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
