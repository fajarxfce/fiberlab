package traffic

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"ftthlab/internal/model"
)

type countedConn struct {
	net.Conn
	read, written *atomic.Uint64
}

func (c countedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.read.Add(uint64(n))
	return n, err
}
func (c countedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written.Add(uint64(n))
	return n, err
}

func origin(t *testing.T) (*Server, string) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := Serve(listener)
	t.Cleanup(server.Close)
	return server, listener.Addr().String()
}

func eventually(t *testing.T, deadline time.Duration, condition func() bool) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true before its deadline")
}

func TestRealTCPRatesDirectionsDurationAndStop(t *testing.T) {
	for _, rates := range [][2]int{{128, 32}, {128, 0}, {0, 32}} {
		t.Run(fmt.Sprint(rates), func(t *testing.T) {
			t.Parallel()
			_, address := origin(t)
			var read, written atomic.Uint64
			var generator Generator
			t.Cleanup(generator.Stop)
			config := model.TrafficConfig{ONUIDs: []string{"onu-1"}, DownloadKbps: rates[0], UploadKbps: rates[1], DurationSeconds: 2}
			err := generator.Start(context.Background(), config, []Target{{ONUID: "onu-1", Dial: func(ctx context.Context) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", address)
				if err != nil {
					return nil, err
				}
				return countedConn{conn, &read, &written}, nil
			}}})
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, 4*time.Second, func() bool { return generator.Snapshot().State == "completed" })
			if generator.Snapshot().StoppedAt == nil {
				t.Fatal("completion time missing")
			}
			// Exclude the ACK/header; the remaining bytes crossed a real socket.
			for i, n := range []uint64{read.Load() - 1, written.Load() - 12} {
				rate := rates[i]
				if rate == 0 && n != 0 {
					t.Fatalf("disabled direction sent %d payload bytes", n)
				}
				if rate > 0 && (n < uint64(rate*125/2) || n > uint64(rate*125*2)) {
					t.Fatalf("unpaced or absent payload: rate %d Kbps, %d bytes in two seconds", rate, n)
				}
			}
			beforeRead, beforeWrite := read.Load(), written.Load()
			generator.Stop()
			time.Sleep(300 * time.Millisecond)
			if read.Load() != beforeRead || written.Load() != beforeWrite {
				t.Fatal("traffic continued after completion")
			}
		})
	}
}

func TestWaitingPathRecoversAndSnapshotsAreIndependent(t *testing.T) {
	t.Parallel()
	_, address := origin(t)
	var attempts atomic.Int32
	var generator Generator
	t.Cleanup(generator.Stop)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := model.TrafficConfig{ONUIDs: []string{"onu-1"}, DownloadKbps: 32, UploadKbps: 8}
	targets := []Target{{ONUID: "onu-1", Dial: func(ctx context.Context) (net.Conn, error) {
		if attempts.Add(1) == 1 {
			return nil, fmt.Errorf("PPP path is down")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	}}}
	if err := generator.Start(ctx, config, targets); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return generator.Snapshot().ONUs[0].Error != "" })
	view := generator.Snapshot()
	if view.ONUs[0].State != "waiting" {
		t.Fatal("unreachable peer reported streaming")
	}
	view.ONUs[0].State, view.Config.ONUIDs[0] = "changed", "changed"
	config.ONUIDs[0] = "changed"
	if generator.Snapshot().Config.ONUIDs[0] != "onu-1" {
		t.Fatal("caller mutated an active job")
	}
	eventually(t, 4*time.Second, func() bool { return generator.Snapshot().ONUs[0].State == "streaming" })
	if generator.Snapshot().ONUs[0].Error != "" {
		t.Fatal("recovery retained a stale error")
	}
	if err := generator.Start(ctx, generator.Snapshot().Config, targets); err == nil {
		t.Fatal("overlapping traffic jobs were accepted")
	}
	cancel()
	eventually(t, time.Second, func() bool { return generator.Snapshot().State == "stopped" })
	if err := generator.Start(context.Background(), generator.Snapshot().Config, targets); err != nil {
		t.Fatal("could not restart stopped traffic:", err)
	}
	generator.Stop()
}

func Test500StreamsCancelTogetherAndReleaseSockets(t *testing.T) {
	server, address := origin(t)
	var generator Generator
	t.Cleanup(generator.Stop)
	config := model.TrafficConfig{DownloadKbps: 8, UploadKbps: 2}
	var targets []Target
	for i := 0; i < model.MaxONUs; i++ {
		id := fmt.Sprintf("onu-%03d", i)
		config.ONUIDs = append(config.ONUIDs, id)
		targets = append(targets, Target{ONUID: id, Dial: func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}})
	}
	if err := generator.Start(context.Background(), config, targets); err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, func() bool {
		for _, target := range generator.Snapshot().ONUs {
			if target.State != "streaming" {
				return false
			}
		}
		return true
	})
	started := time.Now()
	generator.Stop()
	if time.Since(started) > 2*time.Second {
		t.Fatal("stopping traffic serialized waits across 500 clients")
	}
	eventually(t, 2*time.Second, func() bool { server.mu.Lock(); defer server.mu.Unlock(); return len(server.clients) == 0 })
	if generator.Snapshot().State != "stopped" {
		t.Fatal("stopped traffic still reported running")
	}
}

func TestCancelWhileWANDoesNotAnswerHandshake(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() { conn, _ := listener.Accept(); accepted <- conn }()
	var generator Generator
	t.Cleanup(generator.Stop)
	config := model.TrafficConfig{ONUIDs: []string{"onu-1"}, DownloadKbps: 32}
	if err := generator.Start(context.Background(), config, []Target{{ONUID: "onu-1", Dial: func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp4", listener.Addr().String())
	}}}); err != nil {
		t.Fatal(err)
	}
	conn := <-accepted
	if conn == nil {
		t.Fatal("connection was not accepted")
	}
	defer conn.Close()
	started := time.Now()
	generator.Stop()
	if time.Since(started) > time.Second {
		t.Fatal("stop waited for the handshake timeout")
	}
}
