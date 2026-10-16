//go:build linux

package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"ftthlab/internal/model"
	"ftthlab/internal/traffic"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

type TrafficSpec struct {
	LabID string `json:"labId"`
	model.TrafficConfig
}

func (e *Engine) StartTraffic(spec TrafficSpec) error {
	e.ops.Lock()
	defer e.ops.Unlock()
	if err := spec.TrafficConfig.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	lab, phase, ctx, generator := e.lab, e.view.Phase, e.runContext, e.traffic
	e.mu.Unlock()
	if lab.ID != spec.LabID || phase != "running" || ctx == nil || generator == nil {
		return fmt.Errorf("start this lab before generating traffic")
	}
	subscribers := make(map[string]bool, len(lab.Subscribers))
	for _, subscriber := range lab.Subscribers {
		subscribers[subscriber.ONUID] = true
	}
	targets := make([]traffic.Target, 0, len(spec.ONUIDs))
	for _, id := range spec.ONUIDs {
		if !subscribers[id] {
			return fmt.Errorf("%s is not an ONU subscriber in this lab", id)
		}
		client := e.clients[id]
		targets = append(targets, traffic.Target{ONUID: id, Dial: func(ctx context.Context) (net.Conn, error) {
			if client == nil {
				return nil, fmt.Errorf("ONU has no client; connect its optical path and restart the lab")
			}
			return dialNamespace(ctx, filepath.Join("/var/run/netns", client.Namespace), traffic.Address)
		}})
	}
	if err := generator.Start(ctx, spec.TrafficConfig, targets); err != nil {
		return err
	}
	e.Event("traffic", "info", "WAN traffic", fmt.Sprintf("Started %d ONU streams: %d Kbps download / %d Kbps upload per ONU", len(targets), spec.DownloadKbps, spec.UploadKbps))
	return nil
}

func (e *Engine) StopTraffic(labID string) error {
	e.ops.Lock()
	defer e.ops.Unlock()
	e.mu.Lock()
	current, generator := e.lab.ID, e.traffic
	e.mu.Unlock()
	if current != labID {
		return fmt.Errorf("this lab does not own the running traffic")
	}
	if generator != nil {
		generator.Stop()
	}
	return nil
}

// Only socket creation needs setns: the kernel retains that network namespace
// on the socket, including for connect and all subsequent Go netpoll I/O.
// DNS, host routes, the management network, and HTTP proxies cannot bypass PPP.
func dialNamespace(ctx context.Context, namespace, address string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	remote, err := netip.ParseAddrPort(address)
	if err != nil || !remote.Addr().Is4() {
		return nil, fmt.Errorf("traffic destination must be an IPv4 address")
	}
	fd, err := namespaceSocket(namespace)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "fiberlab-traffic")
	defer file.Close()
	sockaddr := &unix.SockaddrInet4{Port: int(remote.Port()), Addr: remote.Addr().As4()}
	err = unix.Connect(fd, sockaddr)
	if err != nil && !errors.Is(err, unix.EINPROGRESS) {
		return nil, err
	}
	for err != nil {
		if cancelErr := ctx.Err(); cancelErr != nil {
			return nil, cancelErr
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
		ready, pollErr := unix.Poll(poll, 50)
		if errors.Is(pollErr, unix.EINTR) {
			continue
		}
		if pollErr != nil {
			return nil, pollErr
		}
		if ready == 0 {
			continue
		}
		code, socketErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
		if socketErr != nil {
			return nil, socketErr
		}
		if code != 0 {
			return nil, unix.Errno(code)
		}
		err = nil
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return net.FileConn(file)
}

func namespaceSocket(path string) (int, error) {
	type result struct {
		fd  int
		err error
	}
	ready := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		restored := true
		defer func() {
			if restored {
				runtime.UnlockOSThread()
			}
			// On a failed restore, exiting this locked goroutine retires the
			// OS thread instead of letting another goroutine inherit an ONU.
		}()
		original, err := netns.Get()
		if err != nil {
			ready <- result{-1, err}
			return
		}
		defer original.Close()
		target, err := netns.GetFromPath(path)
		if err != nil {
			ready <- result{-1, err}
			return
		}
		defer target.Close()
		if err = netns.Set(target); err != nil {
			ready <- result{-1, err}
			return
		}
		restored = false
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
		if restoreErr := netns.Set(original); restoreErr != nil {
			if fd >= 0 {
				unix.Close(fd)
			}
			ready <- result{-1, fmt.Errorf("restore network namespace: %w", restoreErr)}
			return
		}
		restored = true
		ready <- result{fd, err}
	}()
	resultValue := <-ready
	return resultValue.fd, resultValue.err
}

func observedRate(current, previous uint64, elapsed time.Duration) uint64 {
	if current < previous || elapsed <= 0 {
		return 0
	}
	return uint64(float64(current-previous) * 8 / elapsed.Seconds())
}
