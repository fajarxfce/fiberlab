//go:build linux

package engine

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ftthlab/internal/model"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func (e *Engine) startClients(ctx context.Context) error {
	dir := filepath.Join(e.Dir, "ppp")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	states := model.Derive(e.lab, true)
	for i, sub := range e.lab.Subscribers {
		if err := ctx.Err(); err != nil {
			return err
		}
		node, _ := e.lab.Node(sub.ONUID)
		state := states[sub.ONUID]
		if state.OLTID == "" {
			e.Event("pppoe", "warning", node.Label, "No optical path to OLT; client is not launched")
			continue
		}
		ns := fmt.Sprintf("ftl-%s-%d", strings.TrimPrefix(e.journal.RunID, "run-")[:6], i)
		rootIF, peerIF := fmt.Sprintf("flab-o%d", i), fmt.Sprintf("flab-p%d", i)
		if _, err := command(ctx, "ip", "netns", "add", ns); err != nil {
			return err
		}
		e.journal.Namespaces = append(e.journal.Namespaces, ns)
		if err := e.writeJournal(); err != nil {
			return err
		}
		if err := e.iface(ctx, rootIF, "type", "veth", "peer", "name", peerIF); err != nil {
			return err
		}
		if _, err := command(ctx, "ip", "link", "set", peerIF, "netns", ns); err != nil {
			return err
		}
		if _, err := command(ctx, "ip", "-n", ns, "link", "set", peerIF, "name", "eth0"); err != nil {
			return err
		}
		if node.Config.MAC != "" {
			if _, err := net.ParseMAC(node.Config.MAC); err != nil {
				return fmt.Errorf("invalid ONU client MAC")
			}
			if _, err := command(ctx, "ip", "-n", ns, "link", "set", "eth0", "address", node.Config.MAC); err != nil {
				return err
			}
		}
		if _, err := command(ctx, "ip", "-n", ns, "link", "set", "lo", "up"); err != nil {
			return err
		}
		if _, err := command(ctx, "ip", "-n", ns, "link", "set", "eth0", "up"); err != nil {
			return err
		}
		if err := e.attach(ctx, rootIF, e.bridges[state.OLTID]); err != nil {
			return err
		}
		if _, err := command(ctx, "bridge", "vlan", "add", "dev", rootIF, "vid", strconv.Itoa(node.Config.VLAN), "pvid", "untagged"); err != nil {
			return err
		}
		if _, err := command(ctx, "bridge", "link", "set", "dev", rootIF, "isolated", "on"); err != nil {
			return err
		}
		if !state.OpticalUp || !node.Config.Registered {
			if _, err := command(ctx, "ip", "link", "set", rootIF, "down"); err != nil {
				return err
			}
		}
		fd, err := netns.GetFromName(ns)
		if err != nil {
			return err
		}
		handle, err := netlink.NewHandleAt(fd, unix.NETLINK_ROUTE)
		fd.Close()
		if err != nil {
			return err
		}
		pppName := fmt.Sprintf("fp%s%d", strings.TrimPrefix(e.journal.RunID, "run-")[:6], i)
		options := fmt.Sprintf("plugin %s\neth0\nuser %s\npassword %s\nifname %s\nlinkname %s\nnoauth\nnoipdefault\ndefaultroute\nnodefaultroute6\nnoipv6\nrefuse-eap\nrefuse-mschap\nrefuse-mschap-v2\nhide-password\npersist\nmaxfail 0\nholdoff 5\nlcp-echo-interval 5\nlcp-echo-failure 3\nnodetach\nmtu 1492\nmru 1492\nip-up-script /bin/true\nip-down-script /bin/true\nip-pre-up-script /bin/true\n", PPPPlugin(), pppQuote(sub.Username), pppQuote(sub.Password), pppName, ns)
		path := filepath.Join(dir, sub.ONUID+".options")
		if err = os.WriteFile(path, []byte(options), 0600); err != nil {
			handle.Close()
			return err
		}
		process, err := e.spawn(ctx, "ppp", filepath.Join(dir, sub.ONUID+".log"), nil, "ip", "netns", "exec", ns, "pppd", "file", path)
		if err != nil {
			handle.Close()
			return err
		}
		e.clients[sub.ONUID] = &ONUClient{NodeID: sub.ONUID, Namespace: ns, Interface: rootIF, PPPInterface: pppName, Handle: handle, Process: process, LinkUp: state.OpticalUp && node.Config.Registered}
		// A shared optical path captures all descendant Ethernet endpoints.
		for _, edgeID := range state.Path {
			e.opticalInterfaces[edgeID] = append(e.opticalInterfaces[edgeID], rootIF)
		}
		if i%10 == 0 {
			e.mu.Lock()
			e.view.Progress = 40 + (i * 55 / max(1, len(e.lab.Subscribers)))
			e.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-process.Done:
			return fmt.Errorf("PPPoE client %s exited during startup: %s", node.Label, readTail(filepath.Join(dir, sub.ONUID+".log"), 2000))
		case <-time.After(40 * time.Millisecond):
		}
	}
	return nil
}
func pppQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (e *Engine) monitor(ctx context.Context) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		if err := e.sample(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (e *Engine) sample(ctx context.Context) error {
	e.ops.Lock()
	defer e.ops.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	lab := e.Lab()
	old := e.Snapshot()
	previous := map[string]model.Session{}
	for _, s := range old.Sessions {
		previous[s.ONUID] = s
	}
	sessions := []model.Session{}
	metrics := model.Metrics{ConfiguredSessions: len(lab.Subscribers), WorkerRSSBytes: RSS(os.Getpid())}
	for _, router := range e.routers {
		if router.Process == nil {
			continue
		}
		select {
		case <-router.Process.Done:
			return fmt.Errorf("RouterOS process exited: %s", router.Node.Label)
		default:
		}
		metrics.QEMURSSBytes += RSS(router.Process.Cmd.Process.Pid)
	}
	for _, sub := range lab.Subscribers {
		s := model.Session{ONUID: sub.ONUID, Username: sub.Username, Status: "connecting", UpdatedAt: time.Now().UTC()}
		c := e.clients[sub.ONUID]
		if c == nil {
			s.Status = "unconnected"
			sessions = append(sessions, s)
			continue
		}
		metrics.PPPRSSBytes += RSS(c.Process.Cmd.Process.Pid)
		select {
		case <-c.Process.Done:
			s.Status = "error"
			if previous[sub.ONUID].Status != "error" {
				e.Event("pppoe", "error", sub.Username, "PPP client exited; inspect its log with the terminal's ppp log command")
			}
		default:
		}
		links, err := c.Handle.LinkList()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.Status = "unknown"
		} else {
			for _, link := range links {
				if link.Attrs().Name != c.PPPInterface {
					continue
				}
				addrs, err := c.Handle.AddrList(link, netlink.FAMILY_V4)
				if err != nil || len(addrs) == 0 {
					continue
				}
				s.Status = "connected"
				s.Address = addrs[0].IP.String()
				if addrs[0].Peer != nil {
					s.Peer = addrs[0].Peer.IP.String()
				}
				if link.Attrs().Statistics != nil {
					s.RXBytes = link.Attrs().Statistics.RxBytes
					s.TXBytes = link.Attrs().Statistics.TxBytes
				}
				if c.UpSince.IsZero() {
					c.UpSince = time.Now()
				}
				s.UptimeSeconds = int64(time.Since(c.UpSince).Seconds())
				break
			}
		}
		if s.Status != "connected" {
			c.UpSince = time.Time{}
			if !sub.Enabled && lab.Radius.Mode == "builtin" {
				s.Status = "suspended"
			} else if !old.Nodes[sub.ONUID].OpticalUp || !nodeRegistered(lab, sub.ONUID) {
				s.Status = "offline"
			}
		}
		e.mu.Lock()
		account := e.acct[sub.Username]
		e.mu.Unlock()
		s.SessionID = account.SessionID
		s.LastAccounting = account.Kind
		if s.Status == "connected" {
			metrics.ActiveSessions++
		}
		metrics.RXBytes += s.RXBytes
		metrics.TXBytes += s.TXBytes
		sessions = append(sessions, s)
		if p := previous[sub.ONUID]; p.Status != s.Status {
			level := "info"
			if s.Status != "connected" {
				level = "warning"
			}
			e.Event("pppoe", level, sub.Username, "PPP "+s.Status+addressSuffix(s.Address))
		}
	}
	e.mu.Lock()
	e.view.Sessions = sessions
	e.view.Metrics = metrics
	e.mu.Unlock()
	return nil
}
func nodeRegistered(l model.Lab, id string) bool {
	n, ok := l.Node(id)
	return ok && n.Config.Registered
}
func addressSuffix(ip string) string {
	if ip != "" {
		return " · " + ip
	}
	return ""
}
func RSS(pid int) uint64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		return 0
	}
	parts := strings.Fields(string(b))
	if len(parts) < 2 {
		return 0
	}
	n, _ := strconv.ParseUint(parts[1], 10, 64)
	return n * uint64(os.Getpagesize())
}
func (e *Engine) Exec(ctx context.Context, spec ExecSpec) (string, error) {
	e.ops.Lock()
	defer e.ops.Unlock()
	client := e.clients[spec.NodeID]
	if client == nil {
		return "", fmt.Errorf("no active PPPoE client for this ONU")
	}
	args := strings.Fields(spec.Command)
	if len(args) == 0 {
		return "", nil
	}
	if args[0] == "help" {
		return "ping [IPv4]\nhttp [IPv4]\nip addr\nip route\nppp status\nppp log\nCommands run in this customer's actual network namespace.", nil
	}
	var commandArgs []string
	switch {
	case args[0] == "ping" && (len(args) == 1 || len(args) == 2):
		ip := "198.18.0.1"
		if len(args) == 2 {
			ip = args[1]
		}
		if net.ParseIP(ip) == nil || strings.Contains(ip, ":") {
			return "", fmt.Errorf("use an IPv4 address")
		}
		commandArgs = []string{"ping", "-c", "3", "-W", "1", ip}
	case args[0] == "http" && (len(args) == 1 || len(args) == 2):
		ip := "198.18.0.1"
		if len(args) == 2 {
			ip = args[1]
		}
		if net.ParseIP(ip) == nil || strings.Contains(ip, ":") {
			return "", fmt.Errorf("use an IPv4 address")
		}
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		commandArgs = []string{exe, "http-probe", "--target", "http://" + ip + ":8080/"}
	case len(args) == 2 && args[0] == "ip" && (args[1] == "addr" || args[1] == "route"):
		commandArgs = []string{"ip", args[1]}
	case len(args) == 2 && args[0] == "ppp" && args[1] == "status":
		for _, s := range e.Snapshot().Sessions {
			if s.ONUID == spec.NodeID {
				b, _ := json.MarshalIndent(s, "", "  ")
				return string(b), nil
			}
		}
		return "No PPP session observed", nil
	case len(args) == 2 && args[0] == "ppp" && args[1] == "log":
		return readTail(filepath.Join(e.Dir, "ppp", spec.NodeID+".log"), 16384), nil
	default:
		return "", fmt.Errorf("unsupported client command; type help")
	}
	args = append([]string{"netns", "exec", client.Namespace}, commandArgs...)
	output, err := command(ctx, "ip", args...)
	if err != nil && output != "" {
		return output, nil
	}
	return output, err
}

func (e *Engine) Capture(ctx context.Context, spec CaptureSpec) ([]byte, error) {
	select {
	case e.captures <- struct{}{}:
		defer func() { <-e.captures }()
	default:
		return nil, fmt.Errorf("at most two packet captures can run at once")
	}
	e.ops.Lock()
	ifaces := append([]string{}, e.opticalInterfaces[spec.LinkID]...)
	if name := e.linkInterfaces[spec.LinkID]; name != "" {
		ifaces = append(ifaces, name)
	}
	initial := e.Snapshot()
	e.ops.Unlock()
	if len(ifaces) == 0 || initial.Phase != "running" {
		return nil, fmt.Errorf("start the lab and select a connected link before capturing")
	}
	if spec.Seconds < 1 || spec.Seconds > 30 {
		return nil, fmt.Errorf("capture duration must be 1–30 seconds")
	}
	indices := []int{}
	for _, iface := range ifaces {
		link, err := net.InterfaceByName(iface)
		if err != nil {
			return nil, err
		}
		indices = append(indices, link.Index)
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	// Kernel filter admits only lab interfaces, before any frames are copied
	// to userspace. Other host interfaces are never included in a capture.
	filter := interfaceFilter(indices)
	if err = unix.SetsockoptSockFprog(fd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, &unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}); err != nil {
		return nil, err
	}
	if err = unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_ALL)}); err != nil {
		return nil, err
	}
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 1})
	result := make([]byte, 24)
	binary.LittleEndian.PutUint32(result[0:4], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(result[4:6], 2)
	binary.LittleEndian.PutUint16(result[6:8], 4)
	binary.LittleEndian.PutUint32(result[16:20], 65535)
	binary.LittleEndian.PutUint32(result[20:24], 1)
	deadline := time.Now().Add(time.Duration(spec.Seconds) * time.Second)
	buf := make([]byte, 65535)
	for time.Now().Before(deadline) && len(result) < 8<<20 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		e.mu.Lock()
		stillRunning := e.view.RunID == initial.RunID && e.view.Phase == "running"
		e.mu.Unlock()
		if !stillRunning {
			return nil, fmt.Errorf("runtime stopped during capture")
		}
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK || err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		now := time.Now()
		header := make([]byte, 16)
		binary.LittleEndian.PutUint32(header[0:4], uint32(now.Unix()))
		binary.LittleEndian.PutUint32(header[4:8], uint32(now.Nanosecond()/1000))
		binary.LittleEndian.PutUint32(header[8:12], uint32(n))
		binary.LittleEndian.PutUint32(header[12:16], uint32(n))
		result = append(result, header...)
		result = append(result, buf[:n]...)
	}
	e.Event("capture", "info", spec.LinkID, fmt.Sprintf("Captured %d bytes across %d Ethernet endpoints; optical links combine descendant traffic", len(result), len(ifaces)))
	return result, nil
}
func interfaceFilter(indices []int) []unix.SockFilter {
	filter := []unix.SockFilter{{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0xfffff008}} // SKF_AD_IFINDEX
	for _, index := range indices {
		filter = append(filter, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(index), Jf: 1}, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: 65535})
	}
	return append(filter, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: 0})
}
func htons(v uint16) uint16 { return (v<<8)&0xff00 | v>>8 }
func (e *Engine) ClientIDs() []string {
	e.ops.Lock()
	defer e.ops.Unlock()
	out := make([]string, 0, len(e.clients))
	for id := range e.clients {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
