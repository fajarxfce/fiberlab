package protocol

import (
	"bufio"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ftthlab/internal/model"
	"golang.org/x/crypto/ssh"
)

func TestNativeSSHAndTelnetReferenceCLI(t *testing.T) {
	lab := model.Preset(8)
	view := model.Preview(lab)
	view.Nodes = model.Derive(lab, true)
	actions := make(chan model.Action, 2)
	s := &CLIService{NodeID: "olt-1", Lab: func() model.Lab { return lab }, Snapshot: func() model.RuntimeView { return view }, Action: func(a model.Action) error { actions <- a; return nil }}
	signer, err := LoadSigner(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Listen("127.0.0.1:0", "127.0.0.1:0", signer); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	node, _ := lab.Node("olt-1")
	config := &ssh.ClientConfig{User: node.Config.Username, Auth: []ssh.AuthMethod{ssh.Password(node.Config.Password)}, HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()), Timeout: 2 * time.Second}
	client, err := ssh.Dial("tcp", s.listeners[0].Addr().String(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session, _ := client.NewSession()
	out, err := session.CombinedOutput("lab onu list")
	session.Close()
	if err != nil || !strings.Contains(string(out), "HSGQ00000008") {
		t.Fatalf("SSH ONU inventory failed: %s %v", out, err)
	}
	session, _ = client.NewSession()
	out, err = session.CombinedOutput("show gpon onu state all")
	session.Close()
	if err == nil || !strings.Contains(string(out), "UNSUPPORTED") {
		t.Fatal("unverified vendor grammar returned success")
	}
	session, _ = client.NewSession()
	out, err = session.CombinedOutput("lab onu onu-0001 vlan 101")
	session.Close()
	if err != nil {
		t.Fatalf("native SSH configuration failed: %s %v", out, err)
	}
	if a := <-actions; a.Kind != "vlan" || a.TargetID != "onu-0001" || a.Value != "101" {
		t.Fatalf("wrong canonical action: %+v", a)
	}
	conn, err := net.DialTimeout("tcp", s.listeners[1].Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	r := bufio.NewReader(conn)
	until := func(suffix string) string {
		t.Helper()
		var b strings.Builder
		for b.Len() < 32768 {
			c, err := r.ReadByte()
			if err != nil {
				t.Fatalf("Telnet waiting for %q: %v; %q", suffix, err, b.String())
			}
			b.WriteByte(c)
			if strings.HasSuffix(b.String(), suffix) {
				return b.String()
			}
		}
		t.Fatal("unbounded Telnet output")
		return ""
	}
	until("Username: ")
	_, _ = conn.Write([]byte{255, 253, 1, 255, 253, 3})
	io.WriteString(conn, node.Config.Username+"\r")
	until("Password: ")
	io.WriteString(conn, node.Config.Password+"\r\n")
	if text := until("fiberlab# "); strings.Contains(text, node.Config.Password) {
		t.Fatal("Telnet echoed password")
	}
	io.WriteString(conn, "lab system\r\n")
	if text := until("fiberlab# "); !strings.Contains(text, "PON ports: 8") {
		t.Fatalf("Telnet command failed: %q", text)
	}
}
