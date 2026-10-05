package protocol

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"ftthlab/internal/model"
	"golang.org/x/crypto/ssh"
)

type Capability struct {
	Name      string `json:"name"`
	Support   string `json:"support"`
	Reference string `json:"reference"`
}

func Capabilities() []Capability {
	return []Capability{
		{"RouterOS API / SSH / SNMP", "native", "MikroTik CHR v7"},
		{"SNMP v2c system / IF-MIB / IF-X-MIB", "standard", "RFC 3418 / RFC 2863"},
		{"ONU optical and session telemetry", "lab-reference", "FTTHLAB-MIB, experimental 1.3.6.1.4.1.32473.42"},
		{"ONU authorization, VLAN, admin, reboot", "lab-reference", "SSH/Telnet lab command namespace and HTTP API"},
		{"HSGQ proprietary OIDs and command grammar", "unsupported", "Model-specific MIB / CLI fixtures not yet available; no fabricated mappings"},
		{"PAP / CHAP / accounting / Disconnect", "standard", "RFC 2865, 2866, 2869, 5176"},
		{"ONU ACS / TR-069 CWMP", "standard-reference", "CWMP 1.0 / TR-098 subset; Inform, parameters, HTTP Digest connection request and ONU reboot; host management transport"},
	}
}

type CLIService struct {
	NodeID    string
	Lab       func() model.Lab
	Snapshot  func() model.RuntimeView
	Action    func(model.Action) error
	listeners []net.Listener
	mu        sync.Mutex
	conns     map[net.Conn]bool
	closed    bool
	wg        sync.WaitGroup
}

func LoadSigner(path string) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		_, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return nil, e
		}
		der, e := x509.MarshalPKCS8PrivateKey(key)
		if e != nil {
			return nil, e
		}
		b = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		if e = os.WriteFile(path, b, 0600); e != nil {
			return nil, e
		}
	} else if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(b)
}
func (s *CLIService) Listen(sshAddr, telnetAddr string, signer ssh.Signer) error {
	s.conns = map[net.Conn]bool{}
	config := &ssh.ServerConfig{ServerVersion: "SSH-2.0-Fiberlab_reference", MaxAuthTries: 3, PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		n, _ := s.Lab().Node(s.NodeID)
		if !s.up() || subtle.ConstantTimeCompare([]byte(meta.User()), []byte(n.Config.Username)) != 1 || subtle.ConstantTimeCompare(password, []byte(n.Config.Password)) != 1 {
			return nil, fmt.Errorf("authentication failed")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	for index, address := range []string{sshAddr, telnetAddr} {
		listener, err := net.Listen("tcp4", address)
		if err != nil {
			s.Close()
			return err
		}
		s.listeners = append(s.listeners, listener)
		isSSH := index == 0
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				s.mu.Lock()
				if s.closed || len(s.conns) >= 16 {
					s.mu.Unlock()
					conn.Close()
					continue
				}
				s.conns[conn] = true
				s.wg.Add(1)
				s.mu.Unlock()
				go func() {
					defer s.wg.Done()
					defer func() { conn.Close(); s.mu.Lock(); delete(s.conns, conn); s.mu.Unlock() }()
					if isSSH {
						s.serveSSH(conn, config)
					} else {
						s.serveTelnet(conn)
					}
				}()
			}
		}()
	}
	return nil
}
func (s *CLIService) Close() {
	s.mu.Lock()
	s.closed = true
	for _, l := range s.listeners {
		l.Close()
	}
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *CLIService) up() bool { return s.Snapshot().Nodes[s.NodeID].Status == "online" }
func (s *CLIService) serveSSH(raw net.Conn, config *ssh.ServerConfig) {
	_ = raw.SetDeadline(time.Now().Add(15 * time.Second))
	conn, channels, requests, err := ssh.NewServerConn(raw, config)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = raw.SetDeadline(time.Now().Add(30 * time.Minute))
	go ssh.DiscardRequests(requests)
	slots := make(chan struct{}, 4)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "Only CLI sessions are supported")
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			newChannel.Reject(ssh.ResourceShortage, "At most four CLI channels per connection")
			continue
		}
		channel, reqs, err := newChannel.Accept()
		if err != nil {
			<-slots
			continue
		}
		s.wg.Add(1)
		go func() {
			defer func() { <-slots }()
			defer s.wg.Done()
			defer channel.Close()
			for req := range reqs {
				switch req.Type {
				case "pty-req", "window-change":
					req.Reply(true, nil)
				case "shell":
					req.Reply(true, nil)
					s.shell(&consoleReader{r: bufio.NewReaderSize(channel, 8192), w: channel})
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
					return
				case "exec":
					var payload struct{ Command string }
					if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
						req.Reply(false, nil)
						return
					}
					req.Reply(true, nil)
					result, ok := s.Execute(payload.Command)
					io.WriteString(channel, result+"\r\n")
					code := uint32(0)
					if !ok {
						code = 1
					}
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
					return
				default:
					req.Reply(false, nil)
				}
			}
		}()
	}
}
func (s *CLIService) serveTelnet(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	r := &consoleReader{r: bufio.NewReaderSize(c, 8192), w: c, telnet: true}
	_, _ = c.Write([]byte{255, 251, 1, 255, 251, 3}) // WILL ECHO, WILL SUPPRESS-GO-AHEAD
	io.WriteString(c, "Fiberlab reference CLI\r\nUsername: ")
	u, err := r.readLine(true)
	if err != nil {
		return
	}
	io.WriteString(c, "Password: ")
	p, err := r.readLine(false)
	if err != nil {
		return
	}
	n, _ := s.Lab().Node(s.NodeID)
	if subtle.ConstantTimeCompare([]byte(u), []byte(n.Config.Username)) != 1 || subtle.ConstantTimeCompare([]byte(p), []byte(n.Config.Password)) != 1 || !s.up() {
		io.WriteString(c, "Authentication failed\r\n")
		return
	}
	_ = c.SetDeadline(time.Now().Add(30 * time.Minute))
	s.shell(r)
}
func (s *CLIService) shell(r *consoleReader) {
	io.WriteString(r.w, "\r\nFiberlab / HSGQ-G08R reference model\r\nType help for supported lab commands.\r\n\r\n")
	for {
		io.WriteString(r.w, "fiberlab# ")
		input, err := r.readLine(true)
		if err != nil {
			return
		}
		line := strings.TrimSpace(input)
		if line == "exit" || line == "quit" {
			return
		}
		result, _ := s.Execute(line)
		io.WriteString(r.w, result+"\r\n")
	}
}
func (s *CLIService) Execute(line string) (string, bool) {
	if len(line) > 8192 {
		return "% Command is too long", false
	}
	if !s.up() {
		return "Device unavailable", false
	}
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) == 0 {
		return "", true
	}
	if parts[0] == "help" || parts[0] == "?" {
		return "Supported reference commands:\r\n  lab system\r\n  lab onu list\r\n  lab onu <id> authorize|deauthorize|enable|disable|reboot\r\n  lab onu <id> vlan <1-4094>\r\n  lab capabilities\r\n  exit\r\nHSGQ vendor commands without verified fixtures return UNSUPPORTED.", true
	}
	if parts[0] != "lab" {
		return "% UNSUPPORTED: HSGQ command has no verified fixture. Type help.", false
	}
	l := s.Lab()
	if len(parts) == 2 && parts[1] == "capabilities" {
		b, _ := json.MarshalIndent(Capabilities(), "", "  ")
		return string(b), true
	}
	if len(parts) == 2 && parts[1] == "system" {
		node, _ := l.Node(s.NodeID)
		return fmt.Sprintf("Name: %s\r\nModel: HSGQ-G08R reference (not firmware)\r\nPON ports: 8\r\nManagement: %s\r\nSNMP: v2c, system/IF-MIB + explicit FTTHLAB-MIB", node.Label, model.ManagementIPs(l)[s.NodeID]), true
	}
	if len(parts) == 3 && parts[1] == "onu" && parts[2] == "list" {
		v := s.Snapshot()
		var b strings.Builder
		b.WriteString("ONU              SERIAL        PON    STATE          RX(dBm) VLAN\r\n")
		for _, n := range l.SortedNodes("onu") {
			state := v.Nodes[n.ID]
			if state.OLTID != s.NodeID {
				continue
			}
			rx := "—"
			if state.RXDBm != nil {
				rx = fmt.Sprintf("%.2f", *state.RXDBm)
			}
			fmt.Fprintf(&b, "%-16s %-12s %-6s %-14s %-7s %d\r\n", n.ID, n.Config.Serial, state.PON, state.Status, rx, n.Config.VLAN)
		}
		return b.String(), true
	}
	if len(parts) >= 4 && parts[1] == "onu" {
		id := parts[2]
		n, ok := l.Node(id)
		if !ok || n.Kind != "onu" || s.Snapshot().Nodes[id].OLTID != s.NodeID {
			return "% ONU does not belong to this OLT", false
		}
		kind := parts[3]
		action := model.Action{Kind: kind, TargetID: id}
		switch kind {
		case "authorize", "deauthorize", "enable", "disable", "reboot":
			if len(parts) != 4 {
				return "% Invalid arguments", false
			}
		case "vlan":
			if len(parts) != 5 {
				return "% Usage: lab onu <id> vlan <1-4094>", false
			}
			action.Value = parts[4]
		default:
			return "% UNSUPPORTED operation", false
		}
		if s.Action == nil {
			return "% Control channel unavailable", false
		}
		if err := s.Action(action); err != nil {
			return "% " + err.Error(), false
		}
		return "OK: " + kind + " " + id, true
	}
	return "% Invalid reference command. Type help.", false
}
