package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"ftthlab/internal/model"
	"ftthlab/internal/protocol"
	"github.com/vishvananda/netlink"
)

type ManagedProcess struct {
	Cmd  *exec.Cmd
	Done chan struct{}
	Log  *boundedLog
	Kind string
	Err  error
}
type ONUClient struct {
	NodeID, Namespace, Interface, PPPInterface string
	Handle                                     *netlink.Handle
	Process                                    *ManagedProcess
	UpSince                                    time.Time
	LinkUp                                     bool
}
type Router struct {
	Node                 model.Node
	Process              *ManagedProcess
	Taps                 []string
	Serial, QMP, Address string
	Paused               bool
}
type Journal struct {
	RunID      string         `json:"runId"`
	LabID      string         `json:"labId"`
	Interfaces []string       `json:"interfaces"`
	Namespaces []string       `json:"namespaces"`
	PIDs       []int          `json:"pids"`
	StartTimes map[int]string `json:"startTimes"`
}
type Engine struct {
	mu                sync.Mutex
	ops               sync.Mutex
	Dir               string
	ImageDir          string
	UID, GID          int
	Groups            []uint32
	lab               model.Lab
	spec              StartSpec
	view              model.RuntimeView
	cancel            context.CancelFunc
	done              chan struct{}
	journal           Journal
	processes         []*ManagedProcess
	routers           map[string]*Router
	clients           map[string]*ONUClient
	bridges           map[string]string
	linkInterfaces    map[string]string
	opticalInterfaces map[string][]string
	linkUp            map[string]bool
	oltIPs            map[string]bool
	snmp              []*protocol.SNMPAgent
	cli               []*protocol.CLIService
	radius            *protocol.RadiusService
	origin            *http.Server
	acct              map[string]protocol.Accounting
	eventSeq          int64
	lastAuth          map[string]string
	captures          chan struct{}
}

func New(dir, imageDir string, uid, gid int, groups []uint32) *Engine {
	return &Engine{Dir: dir, ImageDir: imageDir, UID: uid, GID: gid, Groups: groups, captures: make(chan struct{}, 2), view: model.RuntimeView{Phase: "stopped", Nodes: map[string]model.NodeState{}, Sessions: []model.Session{}}}
}
func (e *Engine) Lab() model.Lab { e.mu.Lock(); defer e.mu.Unlock(); return e.lab }
func (e *Engine) Snapshot() model.RuntimeView {
	e.mu.Lock()
	defer e.mu.Unlock()
	v := e.view
	v.Nodes = make(map[string]model.NodeState, len(e.view.Nodes))
	for k, s := range e.view.Nodes {
		v.Nodes[k] = s
	}
	v.Sessions = append([]model.Session{}, e.view.Sessions...)
	v.Events = append([]model.Event{}, e.view.Events...)
	return v
}
func (e *Engine) Event(kind, level, subject, message string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.eventSeq++
	event := model.Event{Seq: e.eventSeq, LabID: e.lab.ID, At: time.Now().UTC(), Kind: kind, Level: level, Subject: subject, Message: message}
	e.view.Events = append(e.view.Events, event)
	if len(e.view.Events) > 1000 {
		e.view.Events = e.view.Events[len(e.view.Events)-1000:]
	}
}
func (e *Engine) phase(phase, message string, progress int) {
	e.mu.Lock()
	e.view.Phase = phase
	e.view.Message = message
	e.view.Progress = progress
	e.mu.Unlock()
	e.Event("runtime", "info", "Lab", message)
}
func (e *Engine) Start(spec StartSpec) error {
	e.ops.Lock()
	defer e.ops.Unlock()
	if err := RequireRuntime(); err != nil {
		return err
	}
	if err := model.Validate(spec.Lab); err != nil {
		return err
	}
	if len(spec.Lab.SortedNodes("router")) == 0 {
		return fmt.Errorf("add a MikroTik before starting the lab")
	}
	e.mu.Lock()
	phase := e.view.Phase
	e.mu.Unlock()
	if phase != "stopped" && phase != "error" {
		return fmt.Errorf("a lab is already %s", phase)
	}
	base, err := filepath.EvalSymlinks(e.ImageDir)
	if err != nil {
		return err
	}
	path, err := filepath.EvalSymlinks(spec.Image.Path)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(path, base+string(os.PathSeparator)) || !strings.HasSuffix(path, ".img") {
		return fmt.Errorf("CHR image must be a regular raw image inside the configured image directory")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("invalid CHR image")
	}
	spec.Image.Path = path
	if err = e.checkNetworks(); err != nil {
		return err
	}
	if err = os.MkdirAll(e.Dir, 0700); err != nil {
		return err
	}
	spec.Image, err = e.cacheImage(spec.Image)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now().UTC()
	runID := model.NewID("run-")
	e.mu.Lock()
	e.lab = spec.Lab
	e.spec = spec
	e.cancel = cancel
	e.done = make(chan struct{})
	e.view = model.Preview(spec.Lab)
	e.view.RunID = runID
	e.view.Phase = "preparing"
	e.view.StartedAt = &now
	e.journal = Journal{RunID: runID, LabID: spec.Lab.ID, StartTimes: map[int]string{}}
	e.processes = nil
	e.routers = map[string]*Router{}
	e.clients = map[string]*ONUClient{}
	e.bridges = map[string]string{}
	e.linkInterfaces = map[string]string{}
	e.opticalInterfaces = map[string][]string{}
	e.linkUp = map[string]bool{}
	e.oltIPs = map[string]bool{}
	e.acct = map[string]protocol.Accounting{}
	e.lastAuth = map[string]string{}
	e.eventSeq = 0
	e.mu.Unlock()
	if err = e.writeJournal(); err != nil {
		cancel()
		close(e.done)
		e.phase("error", "Cannot record runtime ownership: "+err.Error(), 0)
		return err
	}
	go e.run(ctx)
	return nil
}
func (e *Engine) run(ctx context.Context) {
	defer close(e.done)
	e.ops.Lock()
	err := e.build(ctx)
	e.ops.Unlock()
	if err == nil {
		e.phase("running", "Network runtime is active. Session status comes from kernel PPP interfaces and RADIUS accounting.", 100)
		err = e.monitor(ctx)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		e.Event("runtime", "error", "Lab", err.Error())
	}
	e.mu.Lock()
	e.view.Phase = "stopping"
	e.mu.Unlock()
	e.ops.Lock()
	cleanupErr := e.cleanup()
	e.ops.Unlock()
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	if cleanupErr != nil {
		e.Event("cleanup", "error", "Lab", cleanupErr.Error())
		err = errors.Join(err, cleanupErr)
	}
	e.mu.Lock()
	e.view.Nodes = model.Derive(e.lab, false)
	for i := range e.view.Sessions {
		e.view.Sessions[i].Status = "stopped"
	}
	e.view.Metrics.ActiveSessions = 0
	if err != nil && !errors.Is(err, context.Canceled) {
		e.view.Phase = "error"
		e.view.Message = err.Error()
	} else {
		e.view.Phase = "stopped"
		e.view.Message = "Lab stopped; its network interfaces and processes were removed."
	}
	e.mu.Unlock()
}
func (e *Engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	cancel, done := e.cancel, e.done
	if cancel != nil && done != nil && e.view.Phase != "stopped" && e.view.Phase != "error" {
		e.view.Phase = "stopping"
	}
	e.mu.Unlock()
	if cancel == nil || done == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (e *Engine) build(ctx context.Context) error {
	e.phase("preparing", "Creating isolated access bridges and management interfaces…", 5)
	if err := e.buildNetwork(ctx); err != nil {
		return err
	}
	if err := e.startServices(); err != nil {
		return err
	}
	e.mu.Lock()
	e.view.Nodes = model.Derive(e.lab, true)
	e.mu.Unlock()
	for i, node := range e.lab.SortedNodes("router") {
		if err := ctx.Err(); err != nil {
			return err
		}
		e.phase("booting", "Booting "+node.Label+" and configuring RouterOS…", 15+i*10)
		if err := e.startRouter(ctx, node, i); err != nil {
			return fmt.Errorf("%s: %w", node.Label, err)
		}
	}
	e.phase("connecting", "Starting real PPPoE clients in network namespaces…", 40)
	if err := e.startClients(ctx); err != nil {
		return err
	}
	return e.apply(ctx, e.lab)
}
func (e *Engine) startServices() error {
	lab := e.Lab()
	snapshot := e.Snapshot
	if lab.Radius.Mode == "builtin" {
		allowed := map[string]bool{}
		for _, r := range lab.SortedNodes("router") {
			allowed[model.ManagementIPs(lab)[r.ID]] = true
		}
		e.radius = &protocol.RadiusService{Lab: e.Lab, AllowedNAS: allowed, OnAccounting: e.accounting, OnAuth: func(user string, accepted bool) {
			status := "rejected"
			if accepted {
				status = "accepted"
			}
			e.mu.Lock()
			previous := e.lastAuth[user]
			e.lastAuth[user] = status
			e.mu.Unlock()
			if previous != status {
				level := "info"
				if !accepted {
					level = "warning"
				}
				e.Event("radius", level, user, "RADIUS Access-"+status)
			}
		}}
		if err := e.radius.Listen(net.JoinHostPort("10.203.0.1", strconv.Itoa(lab.Radius.AuthPort)), net.JoinHostPort("10.203.0.1", strconv.Itoa(lab.Radius.AccountingPort))); err != nil {
			return fmt.Errorf("RADIUS listener: %w", err)
		}
	}
	signer, err := protocol.LoadSigner(filepath.Join(e.Dir, "olt-host-key"))
	if err != nil {
		return err
	}
	for _, n := range lab.SortedNodes("olt") {
		ip := model.ManagementIPs(lab)[n.ID]
		agent := &protocol.SNMPAgent{NodeID: n.ID, Lab: e.Lab, Snapshot: snapshot, Started: time.Now()}
		if err = agent.Listen(net.JoinHostPort(ip, "161")); err != nil {
			return err
		}
		e.snmp = append(e.snmp, agent)
		cli := &protocol.CLIService{NodeID: n.ID, Lab: e.Lab, Snapshot: snapshot, Action: e.control}
		if err = cli.Listen(net.JoinHostPort(ip, "22"), net.JoinHostPort(ip, "23"), signer); err != nil {
			return err
		}
		e.cli = append(e.cli, cli)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"service": "Fiberlab test origin", "remote": r.RemoteAddr, "at": time.Now().UTC()})
	})
	mux.HandleFunc("/payload", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
		n = max(1, min(n, 32768))
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(bytes.Repeat([]byte("f"), n))
	})
	listener, err := net.Listen("tcp4", "198.18.0.1:8080")
	if err != nil {
		return err
	}
	e.origin = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	go e.origin.Serve(listener)
	return nil
}
func (e *Engine) accounting(a protocol.Accounting) {
	e.mu.Lock()
	previous := e.acct[a.Username]
	if a.Kind == "Stop" && previous.SessionID != "" && previous.SessionID != a.SessionID {
		e.mu.Unlock()
		return
	}
	e.acct[a.Username] = a
	e.mu.Unlock()
	if a.Kind != "Interim" && (previous.Kind != a.Kind || previous.SessionID != a.SessionID) {
		e.Event("accounting", "info", a.Username, "RADIUS Accounting-"+a.Kind+" · "+a.SessionID)
	}
}
func (e *Engine) control(action model.Action) error {
	e.mu.Lock()
	url, token := e.spec.ControlURL, e.spec.ControlToken
	e.mu.Unlock()
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		return fmt.Errorf("application control URL must use loopback")
	}
	b, _ := json.Marshal(action)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fiberlab-Control", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var p struct{ Error string }
		json.NewDecoder(resp.Body).Decode(&p)
		return fmt.Errorf("%s", p.Error)
	}
	return nil
}
func (e *Engine) Apply(ctx context.Context, lab model.Lab) error {
	e.ops.Lock()
	defer e.ops.Unlock()
	if err := model.Validate(lab); err != nil {
		return err
	}
	e.mu.Lock()
	old := e.lab
	phase := e.view.Phase
	e.mu.Unlock()
	if old.ID != lab.ID || phase != "running" {
		return fmt.Errorf("runtime is not ready for live changes")
	}
	if !SameStructure(old, lab) {
		return fmt.Errorf("stop the lab before editing nodes, connections, credentials, RADIUS configuration or router service VLANs")
	}
	if err := e.apply(ctx, lab); err != nil {
		_ = e.apply(context.Background(), old)
		return err
	}
	return nil
}
func SameStructure(a, b model.Lab) bool {
	if len(a.Nodes) != len(b.Nodes) || !reflect.DeepEqual(a.Links, b.Links) || len(a.Subscribers) != len(b.Subscribers) || !reflect.DeepEqual(a.Radius, b.Radius) || a.ImageID != b.ImageID {
		return false
	}
	for _, n := range a.Nodes {
		other, ok := b.Node(n.ID)
		if !ok || n.Kind != other.Kind {
			return false
		}
		x, y := n.Config, other.Config
		x.Powered = y.Powered
		x.AdminUp = y.AdminUp
		x.Registered = y.Registered
		x.VLAN = y.VLAN
		x.TxDBm = y.TxDBm
		x.SensitivityDBm = y.SensitivityDBm
		if !reflect.DeepEqual(x, y) {
			return false
		}
	}
	for i, s := range a.Subscribers {
		t := b.Subscribers[i]
		s.Enabled = t.Enabled
		s.RateLimit = t.RateLimit
		if s != t {
			return false
		}
	}
	return true
}
func (e *Engine) spawn(ctx context.Context, kind, logPath string, credential *syscall.Credential, name string, args ...string) (*ManagedProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	log, err := openLog(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(name, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: credential, Pdeathsig: syscall.SIGTERM}
	if err = cmd.Start(); err != nil {
		log.Close()
		return nil, err
	}
	p := &ManagedProcess{Cmd: cmd, Done: make(chan struct{}), Log: log, Kind: kind}
	e.processes = append(e.processes, p)
	e.journal.PIDs = append(e.journal.PIDs, cmd.Process.Pid)
	e.journal.StartTimes[cmd.Process.Pid] = processStartTime(cmd.Process.Pid)
	go func() { p.Err = cmd.Wait(); log.Close(); close(p.Done) }()
	if err = e.writeJournal(); err != nil {
		stopProcess(p)
		return nil, err
	}
	return p, nil
}
func stopProcess(p *ManagedProcess) {
	if p == nil {
		return
	}
	select {
	case <-p.Done:
		return
	default:
	}
	_ = syscall.Kill(-p.Cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-p.Done:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-p.Cmd.Process.Pid, syscall.SIGKILL)
		<-p.Done
	}
}
func (e *Engine) writeJournal() error {
	b, _ := json.MarshalIndent(e.journal, "", "  ")
	path := filepath.Join(e.Dir, "active.json")
	if err := os.WriteFile(path+".tmp", b, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
func (e *Engine) cleanup() error {
	// Signal the whole cohort first: 500 clients share one grace period.
	for _, p := range e.processes {
		select {
		case <-p.Done:
		default:
			_ = syscall.Kill(-p.Cmd.Process.Pid, syscall.SIGTERM)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for _, p := range e.processes {
		timer := time.NewTimer(max(time.Until(deadline), 0))
		select {
		case <-p.Done:
		case <-timer.C:
			_ = syscall.Kill(-p.Cmd.Process.Pid, syscall.SIGKILL)
			<-p.Done
		}
		timer.Stop()
	}
	for _, c := range e.clients {
		if c.Handle != nil {
			c.Handle.Close()
		}
	}
	if e.radius != nil {
		e.radius.Close()
		e.radius = nil
	}
	for _, c := range e.cli {
		c.Close()
	}
	e.cli = nil
	for _, a := range e.snmp {
		a.Close()
	}
	e.snmp = nil
	if e.origin != nil {
		_ = e.origin.Close()
		e.origin = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return e.removeNetwork(ctx, e.journal)
}

func processStartTime(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	end := strings.LastIndex(string(b), ")")
	if end < 0 {
		return ""
	}
	fields := strings.Fields(string(b[end+1:]))
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}
func command(ctx context.Context, name string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var out cappedBuffer
	out.Max = 65536
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

type cappedBuffer struct {
	bytes.Buffer
	Max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	room := b.Max - b.Len()
	if room > 0 {
		b.Buffer.Write(p[:min(room, n)])
	}
	return n, nil
}
func readTail(path string, maxBytes int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	offset := max(int64(0), info.Size()-int64(maxBytes))
	_, _ = f.Seek(offset, io.SeekStart)
	b, _ := io.ReadAll(io.LimitReader(f, int64(maxBytes)))
	return string(b)
}
