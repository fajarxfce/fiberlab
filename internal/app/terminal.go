package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"ftthlab/internal/model"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

func (a *App) terminal(w http.ResponseWriter, r *http.Request) {
	l, err := a.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	if a.runtime(l).Phase != "running" {
		fail(w, 409, fmt.Errorf("start the runtime before opening a terminal"))
		return
	}
	node, ok := l.Node(r.URL.Query().Get("node"))
	if !ok || (node.Kind != "router" && node.Kind != "olt") {
		fail(w, 400, fmt.Errorf("SSH terminal requires a router or OLT"))
		return
	}
	address := net.JoinHostPort(model.ManagementIPs(l)[node.ID], "22")
	// The disposable lab endpoint is pinned by the local runtime's management
	// network. We do not connect to arbitrary user-supplied SSH destinations.
	config := &ssh.ClientConfig{User: node.Config.Username, Auth: []ssh.AuthMethod{ssh.Password(node.Config.Password)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second}
	client, err := ssh.Dial("tcp", address, config)
	if err != nil {
		fail(w, 502, fmt.Errorf("device SSH: %w", err))
		return
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		fail(w, 502, err)
		return
	}
	defer session.Close()
	if err = session.RequestPty("xterm", 28, 90, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		fail(w, 502, err)
		return
	}
	input, err := session.StdinPipe()
	if err != nil {
		fail(w, 502, err)
		return
	}
	output, err := session.StdoutPipe()
	if err != nil {
		fail(w, 502, err)
		return
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		fail(w, 502, err)
		return
	}
	if err = session.Shell(); err != nil {
		fail(w, 502, err)
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: a.validOrigin, ReadBufferSize: 8192, WriteBufferSize: 8192}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ws.SetReadLimit(16384)
	var mu sync.Mutex
	done := make(chan struct{}, 2)
	pipe := func(reader io.Reader) {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 4096)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				mu.Lock()
				_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
				writeErr := ws.WriteMessage(websocket.BinaryMessage, buf[:n])
				mu.Unlock()
				if writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go pipe(output)
	go pipe(stderr)
	go func() { <-done; ws.Close() }()
	for {
		_, b, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var message struct {
			Type string `json:"type"`
			Data string `json:"data"`
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
		}
		if json.Unmarshal(b, &message) != nil {
			continue
		}
		switch message.Type {
		case "input":
			if _, err = input.Write([]byte(message.Data)); err != nil {
				return
			}
		case "resize":
			if message.Cols > 0 && message.Cols <= 400 && message.Rows > 0 && message.Rows <= 200 {
				_ = session.WindowChange(message.Rows, message.Cols)
			}
		}
	}
}
