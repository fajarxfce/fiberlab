//go:build linux

package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"ftthlab/internal/model"
	"golang.org/x/sys/unix"
)

// Run the compiled test binary inside `unshare --user --map-root-user --net`.
// These are genuine Linux bridge/TAP/raw-packet tests; they do not use /dev/ppp.
func TestKernelVLANFaultsAndPCAP(t *testing.T) {
	if os.Getenv("FIBERLAB_KERNEL_TEST") != "1" {
		t.Skip("opt-in test requires a private network namespace")
	}
	mapping, _ := os.ReadFile("/proc/self/uid_map")
	fields := strings.Fields(string(mapping))
	if os.Geteuid() != 0 || len(fields) != 3 || fields[0] != "0" || fields[1] == "0" || fields[2] != "1" {
		t.Fatal("run inside unshare --user --map-root-user --net as an ordinary user")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	lab := model.Preset(2)
	e := New(t.TempDir(), "", os.Getuid(), os.Getgid(), nil)
	e.lab = lab
	e.view = model.Preview(lab)
	e.view.Phase, e.view.RunID = "running", "run-kernel"
	now := time.Now()
	e.view.StartedAt = &now
	e.journal = Journal{RunID: "run-kernel", StartTimes: map[int]string{}}
	e.routers, e.clients = map[string]*Router{}, map[string]*ONUClient{}
	e.bridges, e.linkInterfaces = map[string]string{}, map[string]string{}
	e.opticalInterfaces = map[string][]string{}
	e.linkUp, e.oltIPs = map[string]bool{}, map[string]bool{}
	defer func() {
		if err := e.cleanup(); err != nil {
			t.Error(err)
		}
	}()
	if err := e.buildNetwork(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.iface(ctx, "flab-o-test", "type", "veth", "peer", "name", "flab-p-test"); err != nil {
		t.Fatal(err)
	}
	if err := e.attach(ctx, "flab-o-test", e.bridges["olt-1"]); err != nil {
		t.Fatal(err)
	}
	mustCommand := func(args ...string) {
		t.Helper()
		if _, err := command(ctx, args[0], args[1:]...); err != nil {
			t.Fatal(err)
		}
	}
	mustCommand("ip", "link", "set", "flab-p-test", "up")
	mustCommand("bridge", "vlan", "add", "dev", "flab-o-test", "vid", "100", "pvid", "untagged")
	e.clients["onu-0001"] = &ONUClient{NodeID: "onu-0001", Interface: "flab-o-test", LinkUp: true}
	e.opticalInterfaces["drop-0001"] = []string{"flab-o-test"}
	tapName := e.routers["router-1"].Taps[2]
	mustCommand("bridge", "vlan", "del", "dev", tapName, "vid", "1-4094")
	mustCommand("bridge", "vlan", "add", "dev", tapName, "vid", "100")
	tap, err := os.OpenFile("/dev/net/tun", os.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()
	ifr, err := unix.NewIfreq(tapName)
	if err != nil {
		t.Fatal(err)
	}
	ifr.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI)
	if err = unix.IoctlIfreq(int(tap.Fd()), unix.TUNSETIFF, ifr); err != nil {
		t.Fatal(err)
	}
	peer, err := net.InterfaceByName("flab-p-test")
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	payload := []byte("fiberlab-real-padi")
	frame := append([]byte{255, 255, 255, 255, 255, 255, 2, 70, 84, 0, 0, 1, 0x88, 0x63, 0x11, 0x09, 0, 0, 0, byte(4 + len(payload)), 1, 1, 0, byte(len(payload))}, payload...)
	send := func() error {
		return unix.Sendto(fd, frame, 0, &unix.SockaddrLinklayer{Ifindex: peer.Index, Protocol: htons(0x8863)})
	}
	receive := func(timeout time.Duration) []byte {
		deadline := time.Now().Add(timeout)
		buf := make([]byte, 2048)
		for time.Now().Before(deadline) {
			ready := []unix.PollFd{{Fd: int32(tap.Fd()), Events: unix.POLLIN}}
			_, err := unix.Poll(ready, 30)
			if err != nil {
				t.Fatal(err)
			}
			if ready[0].Revents&unix.POLLIN == 0 {
				continue
			}
			n, _ := unix.Read(int(tap.Fd()), buf)
			if n > 0 && bytes.Contains(buf[:n], payload) {
				return append([]byte{}, buf[:n]...)
			}
		}
		return nil
	}
	if err = send(); err != nil {
		t.Fatal(err)
	}
	packet := receive(time.Second)
	if len(packet) < 18 || binary.BigEndian.Uint16(packet[12:14]) != 0x8100 || binary.BigEndian.Uint16(packet[14:16])&4095 != 100 || binary.BigEndian.Uint16(packet[16:18]) != 0x8863 {
		t.Fatalf("PADI did not traverse the actual access bridge with VLAN 100: %x", packet)
	}
	// VLAN changes reach the kernel and exclude the packet from this trunk.
	changed := cloneLab(t, lab)
	for i := range changed.Nodes {
		if changed.Nodes[i].ID == "onu-0001" {
			changed.Nodes[i].Config.VLAN = 999
		}
	}
	if err = e.Apply(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if err = send(); err != nil {
		t.Fatal(err)
	}
	if packet = receive(150 * time.Millisecond); packet != nil {
		t.Fatalf("VLAN mismatch leaked a PADI: %x", packet)
	}
	if err = e.Apply(ctx, lab); err != nil {
		t.Fatal(err)
	}
	// The same real packets are available as standard Ethernet PCAP records.
	type captureResult struct {
		data []byte
		err  error
	}
	result := make(chan captureResult, 1)
	go func() {
		data, err := e.Capture(ctx, CaptureSpec{LinkID: "drop-0001", Seconds: 1})
		result <- captureResult{data, err}
	}()
	for i := 0; i < 8; i++ {
		time.Sleep(40 * time.Millisecond)
		if err = send(); err != nil {
			t.Fatal(err)
		}
	}
	capture := <-result
	if capture.err != nil || len(capture.data) < 24 || binary.LittleEndian.Uint32(capture.data[:4]) != 0xa1b2c3d4 || !bytes.Contains(capture.data, payload) {
		t.Fatalf("real Ethernet PCAP failed (%d bytes): %v", len(capture.data), capture.err)
	}
	for receive(50*time.Millisecond) != nil {
	}
	changed = cloneLab(t, lab)
	changed.Faults = []model.Fault{{ID: "fault-cut", Kind: "cable_cut", TargetType: "link", TargetID: "drop-0001"}}
	if err = e.Apply(ctx, changed); err != nil {
		t.Fatal(err)
	}
	_ = send() // The peer may report ENETDOWN, which also proves the path is down.
	if packet = receive(150 * time.Millisecond); packet != nil {
		t.Fatalf("fiber-cut ONU still forwarded: %x", packet)
	}
	if err = e.Apply(ctx, lab); err != nil {
		t.Fatal(err)
	}
	if err = send(); err != nil {
		t.Fatal(err)
	}
	if receive(time.Second) == nil {
		t.Fatal("repair did not restore actual forwarding")
	}
	t.Log("Kernel bridge VLAN tagging/filtering, cut/repair and PCAP verified with real PPPoE discovery frames")
}
