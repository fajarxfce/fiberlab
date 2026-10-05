package cwmp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ftthlab/internal/model"
)

func testHooks() Hooks {
	return Hooks{Load: func(context.Context, string, string) ([]byte, error) { return nil, nil }, Save: func(context.Context, string, string, []byte) error { return nil }}
}

func testCPE(endpoint string) *cpe {
	l := model.Preset(1)
	cfg := model.DefaultACS()
	cfg.Enabled = true
	cfg.URL = endpoint
	n, _ := l.Node("onu-0001")
	return &cpe{input: Input{LabID: l.ID, Node: n, Subscriber: l.Subscribers[0], Config: cfg}, state: persistentState{Parameters: map[string]string{}}, born: time.Now(), events: []event{{Code: "1 BOOT"}}}
}

func decodeForTest(t *testing.T, rpc string) envelope {
	t.Helper()
	e, err := parseEnvelope(soap("acs-task", rpc))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestSessionInteroperableSOAPCookiesAndAtomicParameters(t *testing.T) {
	var saved []byte
	hooks := testHooks()
	hooks.Save = func(_ context.Context, _, _ string, b []byte) error { saved = append([]byte(nil), b...); return nil }
	var request int
	var serverErr string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if request == 0 {
			e, err := parseEnvelope(body)
			if err != nil || e.Body.Messages[0].XMLName.Local != "Inform" || !strings.Contains(string(body), "0 BOOTSTRAP") || !strings.Contains(string(body), "1 BOOT") {
				serverErr = "missing valid bootstrap Inform"
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "fixture", Path: "/"})
			w.Write(soap(e.Header.ID, `<cwmp:InformResponse><MaxEnvelopes>1</MaxEnvelopes></cwmp:InformResponse>`))
		} else {
			cookie, err := r.Cookie("session")
			if err != nil || cookie.Value != "fixture" {
				serverErr = "session cookie was not retained"
			}
			switch request {
			case 1:
				if len(body) != 0 {
					serverErr = "CPE did not send empty POST after InformResponse"
				}
				w.Write(soap("set-good", `<cwmp:SetParameterValues>`+parameterList(map[string]parameter{wlan + "SSID": {Value: "Rumah Test", Type: "string"}, management + "PeriodicInformInterval": {Value: "10", Type: "unsignedInt"}})+`<ParameterKey>valid</ParameterKey></cwmp:SetParameterValues>`))
			case 2:
				if !strings.Contains(string(body), "SetParameterValuesResponse") {
					serverErr = "valid parameter write was not acknowledged"
				}
				w.Write(soap("set-invalid", `<cwmp:SetParameterValues>`+parameterList(map[string]parameter{wlan + "SSID": {Value: "Must roll back", Type: "string"}, root + "Missing": {Value: "x", Type: "string"}})+`<ParameterKey>invalid</ParameterKey></cwmp:SetParameterValues>`))
			case 3:
				if !strings.Contains(string(body), "<FaultCode>9005</FaultCode>") {
					serverErr = "unknown parameter did not return a per-parameter fault"
				}
				w.Write(soap("read", `<cwmp:GetParameterValues><ParameterNames><string>`+wlan+`SSID</string><string>`+management+`Password</string></ParameterNames></cwmp:GetParameterValues>`))
			case 4:
				if !strings.Contains(string(body), "Rumah Test") || strings.Contains(string(body), "Must roll back") || strings.Contains(string(body), "do-not-disclose") {
					serverErr = "atomic write or write-only password contract failed"
				}
				w.WriteHeader(204)
			default:
				serverErr = "unexpected extra request"
				w.WriteHeader(500)
			}
		}
		request++
	}))
	defer server.Close()
	c := testCPE(server.URL)
	c.input.Config.Password = "do-not-disclose"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.run(ctx, http.DefaultTransport.(*http.Transport), hooks); err != nil {
		t.Fatal(err)
	}
	if serverErr != "" {
		t.Fatal(serverErr)
	}
	if request != 5 || c.status.InformCount != 1 || c.status.LastInform == nil {
		t.Fatalf("incomplete CWMP session: %d requests", request)
	}
	var state persistentState
	if err := json.Unmarshal(saved, &state); err != nil {
		t.Fatal(err)
	}
	if !state.Bootstrapped || state.Parameters[wlan+"SSID"] != "Rumah Test" || state.ParameterKey != "valid" {
		t.Fatalf("wrong persisted state: %+v", state)
	}
}

func TestRPCNamesFaultsAndPasswordRules(t *testing.T) {
	c := testCPE("http://127.0.0.1:7547")
	if !strings.Contains(deviceID(c.input), "%2D") || strings.Contains(serial(c.input), "%2D") {
		t.Fatal("GenieACS ID and native CWMP serial use different escaping")
	}
	hooks := testHooks()
	for _, tc := range []struct{ rpc, want string }{
		{`<cwmp:GetParameterNames><ParameterPath></ParameterPath><NextLevel>1</NextLevel></cwmp:GetParameterNames>`, `<Name>InternetGatewayDevice.</Name>`},
		{`<cwmp:GetParameterNames><ParameterPath>` + wlan + `</ParameterPath><NextLevel>true</NextLevel></cwmp:GetParameterNames>`, `<Name>` + wlan + `SSID</Name><Writable>true</Writable>`},
		{`<cwmp:Download/>`, `<FaultCode>9000</FaultCode>`},
		{`<cwmp:GetParameterValues><ParameterNames><string>Device.NoSuchParameter</string></ParameterNames></cwmp:GetParameterValues>`, `<FaultCode>9005</FaultCode>`},
		{`<cwmp:SetParameterValues>` + parameterList(map[string]parameter{wan + "Username": {Value: "changed", Type: "string"}}) + `</cwmp:SetParameterValues>`, `<FaultCode>9008</FaultCode>`},
		{`<cwmp:SetParameterValues>` + parameterList(map[string]parameter{wlan + "Enable": {Value: "true", Type: "string"}}) + `</cwmp:SetParameterValues>`, `<FaultCode>9006</FaultCode>`},
		{`<cwmp:SetParameterValues>` + parameterList(map[string]parameter{wlan + "SSID": {Value: strings.Repeat("x", 33), Type: "string"}}) + `</cwmp:SetParameterValues>`, `<FaultCode>9007</FaultCode>`},
	} {
		body, _ := c.handleRPC(context.Background(), decodeForTest(t, tc.rpc), hooks)
		if !strings.Contains(string(body), tc.want) {
			t.Fatalf("wanted %s, got %s", tc.want, body)
		}
	}
	c.state.Parameters[management+"Password"] = "secret-server"
	c.state.Parameters[wlan+"KeyPassphrase"] = "secret-radio"
	values := c.values()
	if values[management+"Password"].Value != "" || values[wlan+"KeyPassphrase"].Value != "" {
		t.Fatal("write-only passwords are readable")
	}
}

func TestHTTPAuthenticationAndConnectionRequestReplay(t *testing.T) {
	for _, scheme := range []string{"Basic", "Digest"} {
		t.Run(scheme, func(t *testing.T) {
			var auth connectionAuth
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if scheme == "Digest" {
					if !auth.check(r, "user", "test-password") {
						auth.challenge(w)
						return
					}
				} else if u, p, ok := r.BasicAuth(); !ok || u != "user" || p != "test-password" {
					w.Header().Set("WWW-Authenticate", `Basic realm="Fiberlab"`)
					w.WriteHeader(401)
					return
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			h := sessionHTTP{client: server.Client(), url: server.URL + "/acs?test=one", username: "user", password: "test-password"}
			for i := 0; i < 2; i++ {
				if _, _, err := h.post(context.Background(), []byte("payload")); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	a := connectionAuth{}
	w := httptest.NewRecorder()
	a.challenge(w)
	client := httpAuth{}
	if err := client.challenge(w.Header()); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:7548/cwmp/one", nil)
	client.apply(r, "cpe", "request-password")
	if !a.check(r, "cpe", "request-password") || a.check(r, "cpe", "request-password") {
		t.Fatal("Digest connection request was rejected or replayed")
	}
	r2 := httptest.NewRequest("GET", "http://127.0.0.1:7548/cwmp/other", nil)
	r2.Header = r.Header.Clone()
	if a.check(r2, "cpe", "request-password") {
		t.Fatal("Digest authorized a different ONU URI")
	}
	// A separate ACS task/client starts at nc=1. It must obtain a fresh
	// challenge instead of being mistaken for a replay of the previous task.
	w2 := httptest.NewRecorder()
	a.challenge(w2)
	secondClient := httpAuth{}
	if err := secondClient.challenge(w2.Header()); err != nil {
		t.Fatal(err)
	}
	r3 := httptest.NewRequest("GET", "http://127.0.0.1:7548/cwmp/one", nil)
	secondClient.apply(r3, "cpe", "request-password")
	if !a.check(r3, "cpe", "request-password") {
		t.Fatal("fresh ACS task was rejected as a replay")
	}
}

func freeListen(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func TestManagerBounds500SessionsAndStopsOnOutage(t *testing.T) {
	var active atomic.Int32
	var peak atomic.Int32
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		defer active.Add(-1)
		requests.Add(1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	l := model.Preset(500)
	cfg := model.DefaultACS()
	cfg.Enabled = true
	cfg.URL = server.URL
	cfg.ConnectionRequestListen = freeListen(t)
	cfg.ConnectionRequestURL = "http://" + cfg.ConnectionRequestListen
	l.ACS = &cfg
	view := model.Preview(l)
	view.Phase = "running"
	view.RunID = "real-fixture"
	for i := range view.Sessions {
		view.Sessions[i].Status = "connected"
	}
	view.Nodes = model.Derive(l, true)
	m := New(context.Background(), testHooks())
	defer m.Close()
	m.Sync(l, view)
	for _, c := range m.entries {
		c.mu.Lock()
		c.due = time.Now().Add(-time.Second)
		c.mu.Unlock()
	}
	m.Sync(l, view)
	deadline := time.Now().Add(3 * time.Second)
	for requests.Load() < 8 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if requests.Load() != 8 || peak.Load() > 8 {
		t.Fatalf("concurrency is not bounded: requests=%d peak=%d", requests.Load(), peak.Load())
	}
	for i := range view.Sessions {
		view.Sessions[i].Status = "disconnected"
	}
	m.Sync(l, view)
	deadline = time.Now().Add(3 * time.Second)
	for active.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if active.Load() != 0 {
		t.Fatal("outage did not cancel in-flight ACS sessions")
	}
	for id, status := range m.Snapshot(l.ID) {
		if status.Status != "offline" {
			t.Fatalf("%s remained %s", id, status.Status)
		}
	}
	if err := m.Trigger(l.ID, l.Subscribers[0].ONUID); err == nil {
		t.Fatal("offline ONU accepted an Inform trigger")
	}
	view.Phase = "stopped"
	m.Sync(l, view)
	if len(m.Snapshot(l.ID)) != 0 {
		t.Fatal("stopped lab retained active CWMP workers")
	}
}

func TestHTTPRedirectNeverForwardsCredentials(t *testing.T) {
	var reached atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 307) }))
	defer redirect.Close()
	c := testCPE(redirect.URL)
	_, err := c.run(context.Background(), http.DefaultTransport.(*http.Transport), testHooks())
	if err == nil || reached.Load() {
		t.Fatalf("redirect followed: %v, reached=%v", err, reached.Load())
	}
}

func TestDigestRFC2617Vector(t *testing.T) {
	p := map[string]string{"realm": "testrealm@host.com", "nonce": "dcd98b7102dd2f0e8b11d0f600bfb0c093", "uri": "/dir/index.html", "qop": "auth", "nc": "00000001", "cnonce": "0a4f113b"}
	if got := digestResponse(p, "Mufasa", "Circle Of Life", "GET"); got != "6629fae49393a05397450978507c4ef1" {
		t.Fatal(fmt.Sprintf("RFC Digest vector mismatch: %s", got))
	}
}

func TestFailedResetIsNotAcknowledgedAndFailedRebootDoesNotInventBoot(t *testing.T) {
	hooks := testHooks()
	hooks.Reboot = func(context.Context, string, string) error { return fmt.Errorf("NAS unavailable") }
	hooks.Save = func(context.Context, string, string, []byte) error { return fmt.Errorf("storage unavailable") }
	c := testCPE("http://127.0.0.1:7547")
	body, action := c.handleRPC(context.Background(), decodeForTest(t, `<cwmp:FactoryReset/>`), hooks)
	if action != nil || !strings.Contains(string(body), "<FaultCode>9002</FaultCode>") {
		t.Fatal("factory reset falsely acknowledged failed persistence")
	}
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch requests {
		case 0:
			w.Write(soap("inform", `<cwmp:InformResponse><MaxEnvelopes>1</MaxEnvelopes></cwmp:InformResponse>`))
		case 1:
			w.Write(soap("reboot", `<cwmp:Reboot><CommandKey>test</CommandKey></cwmp:Reboot>`))
		default:
			w.WriteHeader(204)
		}
		requests++
	}))
	defer server.Close()
	hooks.Save = testHooks().Save
	m := New(context.Background(), hooks)
	defer m.Close()
	c = testCPE(server.URL)
	c.online = true
	c.events = nil
	c.state.Bootstrapped = true
	c.ctx, c.close = context.WithCancel(context.Background())
	defer c.close()
	born := c.born
	m.slots <- struct{}{}
	m.wg.Add(1)
	ctx, cancel := context.WithCancel(c.ctx)
	m.run(c, ctx, cancel)
	if c.status.Status != "error" || len(c.events) != 0 || !c.born.Equal(born) {
		t.Fatal("failed native reboot invented BOOT or reset uptime")
	}
}

func TestPowerRestoreReportsBootButFiberRepairDoesNot(t *testing.T) {
	lab := model.Preset(1)
	cfg := model.DefaultACS()
	cfg.Enabled = true
	cfg.ConnectionRequestListen = freeListen(t)
	cfg.ConnectionRequestURL = "http://" + cfg.ConnectionRequestListen
	lab.ACS = &cfg
	view := model.Preview(lab)
	view.Phase = "running"
	view.RunID = "fixture"
	view.Sessions[0].Status = "connected"
	view.Nodes = model.Derive(lab, true)
	m := New(context.Background(), testHooks())
	defer m.Close()
	// Hold scheduler slots: this test exercises event semantics, not networking.
	for i := 0; i < cap(m.slots); i++ {
		m.slots <- struct{}{}
	}
	m.Sync(lab, view)
	c := m.entries["onu-0001"]
	c.events = nil
	c.status.InformCount = 1
	born := c.born
	lab.Faults = []model.Fault{{ID: "cut", Kind: "cable_cut", TargetType: "link", TargetID: "drop-0001"}}
	view.Nodes = model.Derive(lab, true)
	m.Sync(lab, view)
	lab.Faults = nil
	view.Nodes = model.Derive(lab, true)
	m.Sync(lab, view)
	for _, e := range c.events {
		if e.Code == "1 BOOT" {
			t.Fatal("fiber repair incorrectly reported a device reboot")
		}
	}
	if !c.born.Equal(born) {
		t.Fatal("fiber repair reset device uptime")
	}
	c.events = nil
	lab.Faults = []model.Fault{{ID: "power", Kind: "power_off", TargetType: "node", TargetID: "onu-0001"}}
	view.Nodes = model.Derive(lab, true)
	m.Sync(lab, view)
	lab.Faults = nil
	view.Nodes = model.Derive(lab, true)
	m.Sync(lab, view)
	boot := false
	for _, e := range c.events {
		if e.Code == "1 BOOT" {
			boot = true
		}
	}
	if !boot || !c.born.After(born) {
		t.Fatal("ONU power restore did not report BOOT and restart uptime")
	}
}
