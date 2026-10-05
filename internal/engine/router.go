package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ftthlab/internal/model"
	"github.com/go-routeros/routeros/v3"
)

func (e *Engine) startRouter(ctx context.Context, node model.Node, index int) error {
	for _, path := range []string{filepath.Join(e.Dir, "routers"), filepath.Join(e.Dir, "routers", e.lab.ID)} {
		if err := os.MkdirAll(path, 0711); err != nil {
			return err
		}
		if err := os.Chmod(path, 0711); err != nil {
			return err
		}
	}
	dir := filepath.Join(e.Dir, "routers", e.lab.ID, node.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("invalid router runtime directory")
	}
	if err = os.Chown(dir, e.UID, e.GID); err != nil {
		return err
	}
	credential := &syscall.Credential{Uid: uint32(e.UID), Gid: uint32(e.GID), Groups: e.Groups}
	// Overlay names include the verified base image hash: changing a CHR image
	// never silently reuses a disk backed by a different version.
	disk := filepath.Join(dir, "disk-"+e.spec.Image.SHA256[:12]+".qcow2")
	if _, err := os.Stat(disk); os.IsNotExist(err) {
		cmd := exec.CommandContext(ctx, "qemu-img", "create", "-f", "qcow2", "-F", "raw", "-b", e.spec.Image.Path, disk)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("create router disk: %w: %s", err, out)
		}
	}
	router := e.routers[node.ID]
	router.Serial = filepath.Join(dir, "serial.sock")
	router.QMP = filepath.Join(dir, "qmp.sock")
	for _, path := range []string{router.Serial, router.QMP} {
		_ = os.Remove(path)
	}
	args := []string{"-name", "fiberlab-" + node.ID, "-machine", "q35,accel=kvm", "-cpu", "host", "-smp", "1", "-m", strconv.Itoa(node.Config.MemoryMB), "-display", "none", "-nodefaults", "-drive", "file=" + disk + ",if=virtio,format=qcow2,cache=none", "-serial", "unix:" + router.Serial + ",server=on,wait=off", "-qmp", "unix:" + router.QMP + ",server=on,wait=off"}
	for i, tap := range router.Taps {
		args = append(args, "-netdev", fmt.Sprintf("tap,id=n%d,ifname=%s,script=no,downscript=no", i, tap), "-device", fmt.Sprintf("virtio-net-pci,netdev=n%d,mac=02:46:54:%02x:00:%02x", i, index+1, i+1))
	}
	logDir := filepath.Join(e.Dir, "logs")
	if err = os.MkdirAll(logDir, 0700); err != nil {
		return err
	}
	logPath := filepath.Join(logDir, e.lab.ID+"-"+node.ID+".log")
	process, err := e.spawn(ctx, "qemu", logPath, credential, "qemu-system-x86_64", args...)
	if err != nil {
		return err
	}
	router.Process = process
	bootCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	if err = BootstrapSerial(bootCtx, router.Serial, node.Config.Username, node.Config.Password, router.Address); err != nil {
		return fmt.Errorf("RouterOS console bootstrap failed: %w; QEMU: %s", err, readTail(logPath, 2048))
	}
	client, err := DialRouter(bootCtx, net.JoinHostPort(router.Address, "8728"), node.Config.Username, node.Config.Password)
	if err != nil {
		return err
	}
	defer client.Close()
	if err = ConfigureRouter(bootCtx, client, e.lab, node, index); err != nil {
		return err
	}
	e.Event("routeros", "info", node.Label, "RouterOS API verified; RADIUS and PPPoE access services configured")
	return nil
}

var ansi = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

func routerQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(s) + `"`
}

// BootstrapSerial is intentionally independent of host networking so it can
// also be integration-tested with an unprivileged QEMU user-network backend.
func BootstrapSerial(ctx context.Context, socket, username, password, address string) error {
	return bootstrapSerial(ctx, socket, username, password, address, nil)
}

func bootstrapSerial(ctx context.Context, socket, username, password, address string, trace io.Writer) error {
	var transcript cappedBuffer
	transcript.Max = 65536
	if trace != nil {
		defer func() { _, _ = io.WriteString(trace, strings.ReplaceAll(transcript.String(), password, "[redacted]")) }()
	}
	var conn net.Conn
	var err error
	for {
		conn, err = net.DialTimeout("unix", socket, time.Second)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	defer conn.Close()
	buf := make([]byte, 8192)
	text := ""
	lastInput := time.Now()
	passwordChanges := 0
	configured := false
	trySavedPassword := false
	_, _ = conn.Write([]byte("\r"))
	for {
		if ctx.Err() != nil {
			last := strings.ReplaceAll(text, password, "[redacted]")
			if len(last) > 600 {
				last = last[len(last)-600:]
			}
			return fmt.Errorf("%w; last console output: %q", ctx.Err(), last)
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		n, readErr := conn.Read(buf)
		if n > 0 {
			if trace != nil {
				_, _ = transcript.Write(buf[:n])
			}
			text += ansi.ReplaceAllString(string(buf[:n]), "")
			if len(text) > 32000 {
				text = text[len(text)-16000:]
			}
		}
		lower := strings.ToLower(text)
		send := ""
		switch {
		case strings.Contains(text, "\nFIBERLAB-BOOT-READY") || strings.Contains(text, "\rFIBERLAB-BOOT-READY"):
			return nil
		case strings.Contains(lower, "login failed"):
			if trySavedPassword {
				return fmt.Errorf("saved RouterOS credentials did not match the overlay; reset this router's overlay to reprovision it")
			}
			trySavedPassword = true
			send = "\r"
		case strings.HasSuffix(strings.TrimSpace(lower), "login:"):
			send = username + "+ct\r"
		case (strings.Contains(lower, "repeat new password") || strings.Contains(lower, "confirm new password")) && (strings.HasSuffix(strings.TrimSpace(lower), ">") || strings.HasSuffix(strings.TrimSpace(lower), ":")):
			send = password + "\r"
			passwordChanges++
		case strings.Contains(lower, "new password>") || strings.Contains(lower, "new password:"):
			send = password + "\r"
			passwordChanges++
		case strings.HasSuffix(strings.TrimSpace(lower), "password:"):
			if configured || passwordChanges > 0 || trySavedPassword {
				send = password + "\r"
			} else {
				send = "\r"
			}
		case strings.Contains(lower, "do you want to see the software license") && strings.HasSuffix(strings.TrimSpace(lower), "[y/n]:"):
			send = "n\r"
		case strings.Contains(lower, "-- press enter (q to abort)"):
			send = "q"
		case strings.Contains(lower, "press enter to continue"):
			send = "\r"
		case strings.Contains(text, "] >") || strings.Contains(text, "]>"):
			if configured {
				break
			}
			configured = true
			send = "/user set [find name=" + routerQuote(username) + "] password=" + routerQuote(password) + "\r" +
				":if ([:len [/ip address find where comment=\"fiberlab-mgmt\"]] = 0) do={ /ip address add address=" + address + "/24 interface=ether1 comment=\"fiberlab-mgmt\" } else={ /ip address set [/ip address find where comment=\"fiberlab-mgmt\"] address=" + address + "/24 interface=ether1 }\r" +
				"/ip service set api disabled=no\r" +
				"/ip service set ssh disabled=no\r" +
				"/ip service set winbox disabled=no port=8291\r" +
				":put \"FIBERLAB-BOOT-READY\"\r"
		}
		if send != "" {
			if trace != nil {
				fmt.Fprintf(&transcript, "\n>> %q\n", send)
			}
			if _, err = conn.Write([]byte(send)); err != nil {
				return err
			}
			text = ""
			lastInput = time.Now()
		}
		if readErr != nil {
			if ne, ok := readErr.(net.Error); !ok || !ne.Timeout() {
				return readErr
			}
			if time.Since(lastInput) > 10*time.Second {
				_, _ = conn.Write([]byte("\r"))
				lastInput = time.Now()
			}
		}
	}
}
func DialRouter(ctx context.Context, address, user, password string) (*routeros.Client, error) {
	for {
		client, err := routeros.DialContext(ctx, address, user, password)
		if err == nil {
			return client, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("RouterOS API unavailable: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}
func ConfigureRouter(ctx context.Context, c *routeros.Client, lab model.Lab, node model.Node, index int) error {
	run := func(args ...string) error { _, err := c.RunArgsContext(ctx, args); return err }
	// Only tagged Fiberlab-managed entries are reconciled. Operator-created
	// RouterOS configuration survives a stop/start in the persistent overlay.
	for _, path := range []string{"/interface/pppoe-server/server", "/interface/vlan", "/ip/address", "/ip/route", "/ip/firewall/nat", "/radius", "/ppp/profile"} {
		reply, err := c.RunArgsContext(ctx, []string{path + "/print", "?comment=fiberlab"})
		if err != nil {
			return err
		}
		for _, entry := range reply.Re {
			if err = run(path+"/remove", "=.id="+entry.Map[".id"]); err != nil {
				return err
			}
		}
	}
	if err := run("/system/identity/set", "=name="+node.Label); err != nil {
		return err
	}
	if err := run("/ip/address/add", "=address="+fmt.Sprintf("198.18.0.%d/24", 10+index), "=interface=ether2", "=comment=fiberlab"); err != nil {
		return err
	}
	if err := run("/ip/firewall/nat/add", "=chain=srcnat", "=src-address=172.30.0.0/22", "=out-interface=ether2", "=action=masquerade", "=comment=fiberlab"); err != nil {
		return err
	}
	if err := run("/ppp/profile/add", "=name=fiberlab-radius", "=local-address=172.30.0.1", "=change-tcp-mss=yes", "=only-one=yes", "=comment=fiberlab"); err != nil {
		return err
	}
	if err := run("/ppp/aaa/set", "=use-radius=yes", "=accounting=yes", "=interim-update="+strconv.Itoa(lab.Radius.InterimSeconds)+"s"); err != nil {
		return err
	}
	address := lab.Radius.Address
	if lab.Radius.Mode == "builtin" {
		address = "10.203.0.1"
	} else if !strings.HasPrefix(address, "10.203.0.") {
		if err := run("/ip/route/add", "=dst-address="+address+"/32", "=gateway=10.203.0.1", "=comment=fiberlab"); err != nil {
			return err
		}
	}
	if err := run("/radius/add", "=service=ppp", "=address="+address, "=secret="+lab.Radius.Secret, "=authentication-port="+strconv.Itoa(lab.Radius.AuthPort), "=accounting-port="+strconv.Itoa(lab.Radius.AccountingPort), "=timeout=2s", "=comment=fiberlab"); err != nil {
		return err
	}
	if err := run("/radius/incoming/set", "=accept=yes", "=port=3799"); err != nil {
		return err
	}
	for port := 1; port <= 4; port++ {
		for _, vlan := range node.Config.ServiceVLANs {
			name := fmt.Sprintf("fl-%d-v%d", port, vlan)
			if err := run("/interface/vlan/add", "=name="+name, "=interface=ether"+strconv.Itoa(port+2), "=vlan-id="+strconv.Itoa(vlan), "=comment=fiberlab"); err != nil {
				return err
			}
			if err := run("/interface/pppoe-server/server/add", "=interface="+name, "=service-name=fiberlab", "=default-profile=fiberlab-radius", "=authentication=pap,chap", "=max-mtu=1492", "=max-mru=1492", "=keepalive-timeout=10", "=one-session-per-host=yes", "=disabled=no", "=comment=fiberlab"); err != nil {
				return err
			}
		}
	}
	if err := run("/snmp/set", "=enabled=yes"); err != nil {
		return err
	}
	communities, err := c.RunArgsContext(ctx, []string{"/snmp/community/print"})
	if err != nil {
		return err
	}
	configured := false
	for _, entry := range communities.Re {
		if entry.Map["name"] == node.Config.Community {
			if err = run("/snmp/community/set", "=.id="+entry.Map[".id"], "=addresses=10.203.0.0/24", "=read-access=yes", "=write-access=no"); err != nil {
				return err
			}
			configured = true
		} else if entry.Map["name"] == "public" {
			if err = run("/snmp/community/set", "=.id="+entry.Map[".id"], "=read-access=no", "=write-access=no"); err != nil {
				return err
			}
		}
	}
	if !configured {
		if err = run("/snmp/community/add", "=name="+node.Config.Community, "=addresses=10.203.0.0/24", "=read-access=yes", "=write-access=no"); err != nil {
			return err
		}
	}
	return nil
}

func QMP(ctx context.Context, socket, operation string) error {
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	decoder := json.NewDecoder(bufio.NewReader(conn))
	encoder := json.NewEncoder(conn)
	var greeting map[string]any
	if err = decoder.Decode(&greeting); err != nil {
		return err
	}
	for _, cmd := range []string{"qmp_capabilities", operation} {
		if err = encoder.Encode(map[string]any{"execute": cmd}); err != nil {
			return err
		}
		for {
			var response map[string]any
			if err = decoder.Decode(&response); err != nil {
				return err
			}
			if failure, ok := response["error"]; ok {
				return fmt.Errorf("QMP %s: %v", cmd, failure)
			}
			if _, ok := response["return"]; ok {
				break
			}
		}
	}
	return nil
}
