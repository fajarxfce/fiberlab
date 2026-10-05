package cwmp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"sync"
	"time"

	"ftthlab/internal/model"
)

type Hooks struct {
	Load   func(context.Context, string, string) ([]byte, error)
	Save   func(context.Context, string, string, []byte) error
	Reboot func(context.Context, string, string) error
	Event  func(labID, level, onuID, message string)
}

type cpe struct {
	mu      sync.Mutex
	input   Input
	state   persistentState
	status  model.ACSStatus
	born    time.Time
	due     time.Time
	online  bool
	powered bool
	busy    bool
	events  []event
	retry   int
	ctx     context.Context
	close   context.CancelFunc
	cancel  context.CancelFunc
	auth    connectionAuth
}

type sessionHTTP struct {
	client                  *http.Client
	auth                    httpAuth
	url, username, password string
}

func (s *sessionHTTP) post(ctx context.Context, body []byte) ([]byte, int, error) {
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
		if err != nil {
			return nil, 0, fmt.Errorf("invalid ACS endpoint")
		}
		req.Header.Set("Content-Type", "text/xml; charset=utf-8")
		req.Header.Set("SOAPAction", `""`)
		req.Header.Set("User-Agent", "Fiberlab-CWMP/0.2.0")
		s.auth.apply(req, s.username, s.password)
		resp, err := s.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, 0, ctx.Err()
			}
			// A URL can contain a query token. Do not include net/http's URL-bearing
			// error string in a runtime status, event, or screenshot.
			return nil, 0, fmt.Errorf("ACS HTTP connection failed; check reachability, TLS certificate and timeout")
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxSOAP+1))
		resp.Body.Close()
		if readErr != nil {
			return nil, resp.StatusCode, fmt.Errorf("could not read ACS response")
		}
		if len(data) > maxSOAP {
			return nil, resp.StatusCode, fmt.Errorf("ACS response exceeds 2 MiB")
		}
		if resp.StatusCode == 401 {
			if len(resp.Header.Values("WWW-Authenticate")) == 0 {
				return nil, 401, fmt.Errorf("ACS rejected HTTP credentials")
			}
			if err := s.auth.challenge(resp.Header); err != nil {
				return nil, 401, err
			}
			continue
		}
		if resp.StatusCode != 200 && resp.StatusCode != 204 {
			return nil, resp.StatusCode, fmt.Errorf("ACS returned HTTP %d", resp.StatusCode)
		}
		return data, resp.StatusCode, nil
	}
	return nil, 401, fmt.Errorf("ACS rejected HTTP credentials")
}

func (c *cpe) run(ctx context.Context, transport *http.Transport, hooks Hooks) (*rpcAction, error) {
	c.mu.Lock()
	in := c.input
	events := append([]event(nil), c.events...)
	sentEvents := len(events)
	if !c.state.Bootstrapped {
		events = append([]event{{Code: "0 BOOTSTRAP"}}, events...)
	}
	if len(events) == 0 {
		events = []event{{Code: "2 PERIODIC"}}
	}
	payload := inform(randomToken(), in, c.values(), events, c.retry)
	endpoint := c.value(management+"URL", in.Config.URL)
	username := c.value(management+"Username", in.Config.Username)
	password := c.value(management+"Password", in.Config.Password)
	c.mu.Unlock()
	jar, _ := cookiejar.New(nil)
	// CWMP authentication and session cookies can be tied to the TCP connection
	// (including GenieACS Digest nonces). Never pool one ONU's session connection
	// with another ONU, while the manager still bounds total concurrency to eight.
	connection := transport.Clone()
	connection.MaxConnsPerHost = 1
	connection.MaxIdleConns = 1
	connection.MaxIdleConnsPerHost = 1
	defer connection.CloseIdleConnections()
	h := sessionHTTP{client: &http.Client{Transport: connection, Jar: jar, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, url: endpoint, username: username, password: password}
	body, _, err := h.post(ctx, payload)
	if err != nil {
		return nil, err
	}
	e, err := parseEnvelope(body)
	if err != nil {
		return nil, err
	}
	if e.Body.Messages[0].XMLName.Local != "InformResponse" {
		return nil, fmt.Errorf("ACS did not acknowledge Inform")
	}
	c.mu.Lock()
	if !c.state.Bootstrapped {
		next := c.state
		next.Bootstrapped = true
		state, _ := json.Marshal(next)
		if err := hooks.Save(ctx, in.LabID, in.Node.ID, state); err != nil {
			c.mu.Unlock()
			return nil, fmt.Errorf("could not persist ACS registration")
		}
		c.state = next
	}
	c.status.InformCount++
	now := time.Now().UTC()
	c.status.LastInform = &now
	// Requests arriving during the session remain queued for a later Inform.
	if sentEvents <= len(c.events) {
		c.events = append([]event(nil), c.events[sentEvents:]...)
	}
	c.mu.Unlock()
	payload = nil
	var action *rpcAction
	for count := 0; count < 128; count++ {
		body, status, err := h.post(ctx, payload)
		if err != nil {
			return action, err
		}
		if status == 204 || len(bytes.TrimSpace(body)) == 0 {
			return action, nil
		}
		e, err = parseEnvelope(body)
		if err != nil {
			return action, err
		}
		if e.Body.Messages[0].XMLName.Local == "Fault" {
			return action, fmt.Errorf("ACS returned a SOAP fault")
		}
		var requested *rpcAction
		payload, requested = c.handleRPC(ctx, e, hooks)
		if requested != nil {
			if action != nil {
				payload = fault(e.Header.ID, 9001, "A reboot is already pending in this session")
			} else {
				action = requested
			}
		}
	}
	return action, fmt.Errorf("ACS session exceeded 128 RPCs")
}

func (c *cpe) queue(e event) {
	for _, pending := range c.events {
		if pending == e {
			return
		}
	}
	if len(c.events) < 16 {
		c.events = append(c.events, e)
	}
	c.due = time.Now()
}

func (c *cpe) interval() time.Duration {
	seconds, err := time.ParseDuration(c.value(management+"PeriodicInformInterval", fmt.Sprint(c.input.Config.PeriodicInformSeconds)) + "s")
	if err != nil || seconds < 10*time.Second || seconds > 24*time.Hour {
		return time.Minute
	}
	return seconds
}
