package cwmp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ftthlab/internal/model"
)

// Manager schedules at most eight concurrent CWMP sessions. Idle ONUs require
// only a small state record, not a process, container, timer or goroutine each.
type Manager struct {
	mu        sync.Mutex
	hooks     Hooks
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	entries   map[string]*cpe
	labID     string
	runID     string
	listen    string
	server    *http.Server
	listenErr string
	transport *http.Transport
	slots     chan struct{}
}

func New(ctx context.Context, hooks Hooks) *Manager {
	ctx, cancel := context.WithCancel(ctx)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxConnsPerHost = 8
	transport.MaxIdleConns = 16
	transport.MaxIdleConnsPerHost = 8
	transport.IdleConnTimeout = 30 * time.Second
	transport.ResponseHeaderTimeout = 15 * time.Second
	return &Manager{ctx: ctx, cancel: cancel, hooks: hooks, entries: map[string]*cpe{}, transport: transport, slots: make(chan struct{}, 8)}
}

func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	m.stopLocked()
	m.mu.Unlock()
	m.wg.Wait()
	m.transport.CloseIdleConnections()
}

func (m *Manager) stopLocked() {
	for _, c := range m.entries {
		c.close()
	}
	m.entries = map[string]*cpe{}
	if m.server != nil {
		_ = m.server.Close()
		m.server = nil
	}
	m.labID = ""
	m.runID = ""
	m.listen = ""
	m.listenErr = ""
}

func effective(l model.Lab, n model.Node) model.ACSConfig {
	c := *l.ACS
	if override := n.Config.ACS; override != nil {
		if override.URL != "" {
			c.URL = override.URL
			c.Username = override.Username
			c.Password = override.Password
		}
		if override.PeriodicInformSeconds > 0 {
			c.PeriodicInformSeconds = override.PeriodicInformSeconds
		}
	}
	return c
}

func settingsKey(c model.ACSConfig) string {
	b, _ := json.Marshal(c)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

func (m *Manager) listenerLocked(c model.ACSConfig) {
	if m.listen == c.ConnectionRequestListen && m.server != nil {
		return
	}
	if m.server != nil {
		_ = m.server.Close()
		m.server = nil
	}
	m.listen = c.ConnectionRequestListen
	listener, err := net.Listen("tcp", m.listen)
	if err != nil {
		m.listenErr = "Connection-request listener could not bind; check the address and port"
		return
	}
	m.listenErr = ""
	server := &http.Server{Handler: http.HandlerFunc(m.connectionRequest), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	m.server = server
	m.wg.Add(1)
	go func() { defer m.wg.Done(); _ = server.Serve(listener) }()
}

// Sync is called by the application's existing runtime poller. No ACS traffic
// can start until a real PPP session and the modeled service path are both up.
func (m *Manager) Sync(l model.Lab, view model.RuntimeView) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return
	}
	if l.ACS == nil || !l.ACS.Enabled || view.Phase != "running" || view.LabID != l.ID {
		m.stopLocked()
		return
	}
	if m.labID != l.ID || m.runID != view.RunID {
		m.stopLocked()
		m.labID = l.ID
		m.runID = view.RunID
	}
	m.listenerLocked(*l.ACS)
	subs := make(map[string]model.Subscriber, len(l.Subscribers))
	for _, s := range l.Subscribers {
		subs[s.ONUID] = s
	}
	sessions := make(map[string]model.Session, len(view.Sessions))
	for _, s := range view.Sessions {
		sessions[s.ONUID] = s
	}
	seen := map[string]bool{}
	powerFaults := map[string]bool{}
	for _, f := range l.Faults {
		if f.Kind == "power_off" && f.TargetType == "node" {
			powerFaults[f.TargetID] = true
		}
	}
	for _, n := range l.Nodes {
		if n.Kind != "onu" || (n.Config.ACS != nil && n.Config.ACS.Disabled) {
			continue
		}
		seen[n.ID] = true
		in := Input{LabID: l.ID, RunID: view.RunID, Node: n, Subscriber: subs[n.ID], Session: sessions[n.ID], NodeState: view.Nodes[n.ID], Config: effective(l, n)}
		online := in.Session.Status == "connected" && in.NodeState.ServicePathUp && in.NodeState.OpticalUp
		powered := n.Config.Powered && !powerFaults[n.ID]
		c := m.entries[n.ID]
		if c == nil {
			ctx, cancel := context.WithCancel(m.ctx)
			c = &cpe{input: in, ctx: ctx, close: cancel, powered: powered, born: time.Now(), events: []event{{Code: "1 BOOT"}}, state: persistentState{Parameters: map[string]string{}}}
			body, err := m.hooks.Load(ctx, l.ID, n.ID)
			if err != nil || (len(body) > 0 && json.Unmarshal(body, &c.state) != nil) {
				c.status.LastError = "Could not load saved ACS parameters"
			}
			if c.state.Parameters == nil {
				c.state.Parameters = map[string]string{}
			}
			h := fnv.New32a()
			_, _ = h.Write([]byte(n.ID))
			c.due = time.Now().Add(time.Duration(h.Sum32()%5000) * time.Millisecond)
			m.entries[n.ID] = c
		}
		c.mu.Lock()
		wasOnline := c.online
		if !c.powered && powered {
			c.born = time.Now()
			c.queue(event{Code: "1 BOOT"})
		}
		c.powered = powered
		c.input = in
		key := settingsKey(in.Config)
		if c.state.SettingsKey != key {
			for name := range c.state.Parameters {
				if strings.HasPrefix(name, management) {
					delete(c.state.Parameters, name)
				}
			}
			c.state.SettingsKey = key
			c.state.Bootstrapped = false
			if c.busy && c.cancel != nil {
				c.cancel()
			}
		}
		c.online = online
		c.status.DeviceID = deviceID(in)
		c.status.ConnectionRequestURL = callbackURL(in)
		if !online {
			if c.cancel != nil {
				c.cancel()
			}
			c.status.Status = "offline"
			c.status.NextInform = nil
		} else if !wasOnline && c.status.InformCount > 0 {
			c.queue(event{Code: "4 VALUE CHANGE"})
		}
		if m.listenErr != "" {
			c.status.LastError = m.listenErr
		}
		if online && !c.busy {
			if c.status.Status == "" || c.status.Status == "offline" {
				c.status.Status = "waiting"
			}
			if !c.due.IsZero() && !time.Now().Before(c.due) {
				select {
				case m.slots <- struct{}{}:
					c.busy = true
					c.status.Status = "informing"
					ctx, cancel := context.WithTimeout(c.ctx, 90*time.Second)
					c.cancel = cancel
					m.wg.Add(1)
					go m.run(c, ctx, cancel)
				default:
				}
			}
		}
		c.mu.Unlock()
	}
	for id, c := range m.entries {
		if !seen[id] {
			c.close()
			delete(m.entries, id)
		}
	}
}

func (m *Manager) run(c *cpe, ctx context.Context, cancel context.CancelFunc) {
	defer m.wg.Done()
	defer func() { <-m.slots }()
	defer cancel()
	action, err := c.run(ctx, m.transport, m.hooks)
	if action != nil && c.ctx.Err() == nil {
		c.mu.Lock()
		in := c.input
		c.mu.Unlock()
		rebootCtx, rebootCancel := context.WithTimeout(c.ctx, 20*time.Second)
		rebootErr := m.hooks.Reboot(rebootCtx, in.LabID, in.Node.ID)
		rebootCancel()
		if rebootErr != nil {
			err = fmt.Errorf("ONU reboot could not be applied to the network runtime")
		} else {
			c.mu.Lock()
			c.born = time.Now()
			c.queue(event{Code: "1 BOOT"})
			if action.Kind == "Reboot" {
				c.queue(event{Code: "M Reboot", Key: action.Key})
			}
			c.mu.Unlock()
		}
	}
	c.mu.Lock()
	c.busy = false
	c.cancel = nil
	oldError := c.status.LastError
	var eventMessage string
	if !c.online || c.ctx.Err() != nil {
		c.status.Status = "offline"
		c.status.NextInform = nil
	} else if err != nil {
		c.retry++
		c.status.Status = "error"
		c.status.LastError = err.Error()
		c.due = time.Now().Add(time.Duration(min(300, 5<<min(c.retry-1, 6))) * time.Second)
		if c.status.LastError != oldError {
			eventMessage = c.status.LastError
		}
	} else {
		c.retry = 0
		c.status.Status = "online"
		c.status.LastError = ""
		if c.value(management+"PeriodicInformEnable", "true") == "true" {
			c.due = time.Now().Add(c.interval())
		} else {
			c.due = time.Time{}
		}
		if len(c.events) > 0 {
			c.due = time.Now().Add(time.Second)
		}
		if c.status.InformCount == 1 {
			eventMessage = "ONU registered with ACS over CWMP"
		}
	}
	if c.online && !c.due.IsZero() {
		due := c.due.UTC()
		c.status.NextInform = &due
	} else {
		c.status.NextInform = nil
	}
	in := c.input
	c.mu.Unlock()
	if eventMessage != "" && m.hooks.Event != nil && c.ctx.Err() == nil {
		level := "info"
		if err != nil {
			level = "warning"
		}
		m.hooks.Event(in.LabID, level, in.Node.ID, eventMessage)
	}
}

func (m *Manager) Snapshot(labID string) map[string]model.ACSStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.labID != labID {
		return nil
	}
	out := make(map[string]model.ACSStatus, len(m.entries))
	for id, c := range m.entries {
		c.mu.Lock()
		out[id] = c.status
		c.mu.Unlock()
	}
	return out
}

func (m *Manager) Trigger(labID, onuID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.entries[onuID]
	if m.labID != labID || c == nil {
		return fmt.Errorf("enable ACS and start this ONU's network runtime first")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.online {
		return fmt.Errorf("ONU needs an active PPP session before contacting ACS")
	}
	c.queue(event{Code: "6 CONNECTION REQUEST"})
	return nil
}

func (m *Manager) connectionRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(405)
		return
	}
	if r.Header.Get("Origin") != "" {
		w.WriteHeader(403)
		return
	}
	m.mu.Lock()
	var c *cpe
	// Compare exact configured URL paths, including an optional reverse-proxy prefix.
	ids := make([]string, 0, len(m.entries))
	for id := range m.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		candidate := m.entries[id]
		candidate.mu.Lock()
		expected, _ := http.NewRequest(http.MethodGet, callbackURL(candidate.input), nil)
		candidate.mu.Unlock()
		if expected != nil && expected.URL.EscapedPath() == r.URL.EscapedPath() {
			c = candidate
			break
		}
	}
	m.mu.Unlock()
	if c == nil {
		w.WriteHeader(404)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	user := c.value(management+"ConnectionRequestUsername", c.input.Config.ConnectionRequestUsername)
	pass := c.value(management+"ConnectionRequestPassword", c.input.Config.ConnectionRequestPassword)
	if !c.auth.check(r, user, pass) {
		c.auth.challenge(w)
		return
	}
	if !c.online || c.ctx.Err() != nil {
		w.WriteHeader(503)
		return
	}
	c.queue(event{Code: "6 CONNECTION REQUEST"})
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(200)
}
