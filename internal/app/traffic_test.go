package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	"ftthlab/internal/engine"
	"ftthlab/internal/model"
)

func TestTrafficAPIValidationAndLabOwnership(t *testing.T) {
	a, handler, lab := fixture(t)
	path := "/api/v1/labs/" + lab.ID + "/traffic"
	config := model.TrafficConfig{ONUIDs: []string{"onu-0001"}, DownloadKbps: 64, UploadKbps: 16, DurationSeconds: 60}
	if response := request(t, handler, "POST", path, config); response.Code != 409 {
		t.Fatal("stopped lab accepted traffic:", response.Code)
	}
	bad := config
	bad.ONUIDs = []string{"router-1"}
	if response := request(t, handler, "POST", path, bad); response.Code != 400 {
		t.Fatal("invalid target accepted:", response.Code)
	}
	if response := request(t, handler, "POST", path, map[string]any{"onuIds": config.ONUIDs, "downloadKbps": 64, "url": "http://example.com"}); response.Code != 400 {
		t.Fatal("arbitrary external target accepted")
	}
	other := model.Preset(1)
	if err := a.Store.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	view := model.Preview(lab)
	view.Phase = "running"
	var mu sync.Mutex
	var starts, stops int
	socket := filepath.Join(t.TempDir(), "helper.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/traffic/start":
			var spec engine.TrafficSpec
			if err := json.NewDecoder(r.Body).Decode(&spec); err != nil || spec.LabID != lab.ID {
				t.Error("wrong lab forwarded to helper")
				w.WriteHeader(400)
				return
			}
			starts++
			view.Traffic = &model.TrafficView{State: "running", Config: spec.TrafficConfig}
		case "/traffic/stop":
			var spec struct {
				LabID string `json:"labId"`
			}
			json.NewDecoder(r.Body).Decode(&spec)
			if spec.LabID != lab.ID {
				t.Error("wrong stop owner")
				w.WriteHeader(400)
				return
			}
			stops++
			view.Traffic = &model.TrafficView{State: "stopped", Config: config}
		case "/status":
		default:
			w.WriteHeader(404)
			return
		}
		write(w, 200, view)
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	// Quiesce the fixture's poller before replacing its helper for this request test.
	a.cancel()
	<-a.pollDone
	a.Helper = engine.NewClient(socket)
	a.viewMu.Lock()
	a.view = view
	a.viewMu.Unlock()
	if response := request(t, handler, "POST", path, config); response.Code != 202 {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := request(t, handler, "POST", path, config); response.Code != 409 {
		t.Fatal("duplicate job accepted")
	}
	if response := request(t, handler, "DELETE", "/api/v1/labs/"+other.ID+"/traffic", nil); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	mu.Lock()
	stoppedOther := stops
	mu.Unlock()
	if stoppedOther != 0 {
		t.Fatal("stopping a different lab stopped the active traffic")
	}
	if response := request(t, handler, "DELETE", path, nil); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if starts != 1 || stops != 1 {
		t.Fatalf("helper calls: %d starts, %d stops", starts, stops)
	}
	stored, err := a.Store.Get(context.Background(), lab.ID)
	if err != nil || stored.Revision != lab.Revision {
		t.Fatal("traffic mutated the saved topology")
	}
}
