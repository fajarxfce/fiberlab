//go:build linux

package engine

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"ftthlab/internal/model"
	"ftthlab/internal/traffic"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

func TestObservedTrafficRateAndCounterReset(t *testing.T) {
	for _, row := range []struct {
		current, previous uint64
		elapsed           time.Duration
		want              uint64
	}{{3000, 0, 3 * time.Second, 8000}, {4000, 1000, 1500 * time.Millisecond, 16000}, {10, 90000, time.Second, 0}, {1000, 10, 0, 0}} {
		if got := observedRate(row.current, row.previous, row.elapsed); got != row.want {
			t.Fatalf("rate = %d, want %d", got, row.want)
		}
	}
}

func TestTrafficCannotTargetAnotherLabOrNonONU(t *testing.T) {
	e := New(t.TempDir(), "", 1000, 1000, nil)
	e.lab, e.view.Phase, e.runContext = model.Preset(1), "running", context.Background()
	e.traffic = &traffic.Generator{}
	config := model.TrafficConfig{ONUIDs: []string{"onu-0001"}, DownloadKbps: 64}
	if err := e.StartTraffic(TrafficSpec{LabID: "another-lab", TrafficConfig: config}); err == nil {
		t.Fatal("cross-lab traffic accepted")
	}
	config.ONUIDs = []string{"router-1"}
	if err := e.StartTraffic(TrafficSpec{LabID: e.lab.ID, TrafficConfig: config}); err == nil {
		t.Fatal("router accepted as an ONU")
	}
	if err := e.StopTraffic("another-lab"); err == nil {
		t.Fatal("cross-lab stop accepted")
	}
	if e.traffic.Snapshot() != nil {
		t.Fatal("rejected request started traffic")
	}
}

// Run with FIBERLAB_KERNEL_TEST=1 inside unshare --user --map-root-user --net.
// Identical loopback addresses in two namespaces prove that the socket cannot
// silently take a host shortcut instead of the ONU's route.
func TestTrafficSocketUsesOnlyTargetNamespace(t *testing.T) {
	if os.Getenv("FIBERLAB_KERNEL_TEST") != "1" {
		t.Skip("requires a private user/network namespace")
	}
	mapping, _ := os.ReadFile("/proc/self/uid_map")
	fields := strings.Fields(string(mapping))
	if os.Geteuid() != 0 || len(fields) != 3 || fields[1] == "0" || fields[2] != "1" {
		t.Fatal("run in unshare --user --map-root-user --net as an ordinary user")
	}
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err = netlink.LinkSetUp(lo); err != nil {
		t.Fatal(err)
	}
	host, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	address := host.Addr().String()
	runtime.LockOSThread()
	original, err := netns.Get()
	if err != nil {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	defer original.Close()
	var target netns.NsHandle
	var listener net.Listener
	func() {
		defer runtime.UnlockOSThread()
		defer func() {
			if err := netns.Set(original); err != nil {
				t.Fatal("namespace restore:", err)
			}
		}()
		target, err = netns.New()
		if err != nil {
			t.Fatal(err)
		}
		lo, err := netlink.LinkByName("lo")
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(lo); err != nil {
			t.Fatal(err)
		}
		listener, err = net.Listen("tcp4", address)
		if err != nil {
			t.Fatal(err)
		}
	}()
	defer target.Close()
	server := traffic.Serve(listener)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dialNamespace(ctx, fmt.Sprintf("/proc/self/fd/%d", int(target)), address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	var header [12]byte
	copy(header[:4], "FTL1")
	binary.BigEndian.PutUint32(header[4:8], 64)
	if _, err = conn.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	var received [65]byte
	if _, err = io.ReadFull(conn, received[:]); err != nil {
		t.Fatal("ONU namespace did not deliver WAN traffic:", err)
	}
	if received[0] != 1 {
		t.Fatal("wrong namespace answered the handshake")
	}
	// The parent listener would have accepted a bypassed connection.
	host.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Millisecond))
	if leaked, err := host.Accept(); err == nil {
		leaked.Close()
		t.Fatal("traffic bypassed the ONU namespace")
	}
	if _, err := dialNamespace(ctx, "/nonexistent-fiberlab-namespace", address); err == nil {
		t.Fatal("missing ONU namespace fell back to host networking")
	}
	current, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if !current.Equal(original) {
		t.Fatal("namespace leaked into the Go runtime")
	}
	t.Log("Real TCP payload crossed the target namespace; same-address host listener received no connection")
}
