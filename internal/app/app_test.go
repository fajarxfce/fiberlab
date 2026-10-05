package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"ftthlab/internal/model"
)

func fixture(t *testing.T) (*App, http.Handler, model.Lab) {
	t.Helper()
	dir := t.TempDir()
	a, err := New(dir, "127.0.0.1:8787", filepath.Join(dir, "absent.sock"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	labs, _ := a.Store.List(context.Background())
	return a, a.Handler(), labs[0]
}
func request(t *testing.T, h http.Handler, method, path string, input any) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, body)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestAPINeverInventsConnectedDevices(t *testing.T) {
	_, h, lab := fixture(t)
	w := request(t, h, "GET", "/api/v1/labs/"+lab.ID+"/runtime", nil)
	var v model.RuntimeView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Phase != "stopped" || v.Metrics.ActiveSessions != 0 {
		t.Fatalf("fabricated runtime: %+v", v)
	}
	for _, s := range v.Sessions {
		if s.Status != "stopped" || s.RXBytes != 0 || s.Address != "" {
			t.Fatalf("fabricated session %+v", s)
		}
	}
	w = request(t, h, "POST", "/api/v1/labs/"+lab.ID+"/start", map[string]any{})
	if w.Code < 400 {
		t.Fatal("start without a real image/helper reported success")
	}
}
func TestAPI500ONUsAndImportExport(t *testing.T) {
	_, h, _ := fixture(t)
	w := request(t, h, "POST", "/api/v1/labs", map[string]any{"name": "500 ONU API test", "count": 500})
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var original model.Lab
	json.Unmarshal(w.Body.Bytes(), &original)
	if len(original.Subscribers) != 500 {
		t.Fatal("500 ONU preset truncated")
	}
	w = request(t, h, "GET", "/api/v1/labs/"+original.ID+"/export", nil)
	var exported model.Lab
	json.Unmarshal(w.Body.Bytes(), &exported)
	w = request(t, h, "POST", "/api/v1/labs/import", exported)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var imported model.Lab
	json.Unmarshal(w.Body.Bytes(), &imported)
	if imported.ID == original.ID || imported.Revision != 1 || len(imported.Subscribers) != 500 || imported.Radius.Secret != original.Radius.Secret {
		t.Fatal("snapshot did not round-trip with a new independent lab ID")
	}
}
func TestAPIOptimisticConcurrency(t *testing.T) {
	_, h, lab := fixture(t)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- request(t, h, "PUT", "/api/v1/labs/"+lab.ID, lab).Code }()
	}
	wg.Wait()
	close(results)
	codes := map[int]int{}
	for code := range results {
		codes[code]++
	}
	if codes[200] != 1 || codes[409] != 1 {
		t.Fatalf("concurrent saves overwrote each other: %v", codes)
	}
}
func TestAPIBillingAndOpticsStaySeparate(t *testing.T) {
	_, h, lab := fixture(t)
	w := request(t, h, "POST", "/api/v1/labs/"+lab.ID+"/actions", model.Action{Kind: "suspend", TargetID: "onu-0001"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request(t, h, "GET", "/api/v1/labs/"+lab.ID+"/runtime", nil)
	var view model.RuntimeView
	json.Unmarshal(w.Body.Bytes(), &view)
	if !view.Nodes["onu-0001"].OpticalUp {
		t.Fatal("billing action created optical LOS")
	}
	w = request(t, h, "POST", "/api/v1/labs/"+lab.ID+"/faults", model.Fault{Kind: "pon_down", TargetType: "port", TargetID: "olt-1:pon1"})
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	w = request(t, h, "GET", "/api/v1/labs/"+lab.ID+"/runtime", nil)
	json.Unmarshal(w.Body.Bytes(), &view)
	if view.Nodes["onu-0001"].OpticalUp || !view.Nodes["onu-0005"].OpticalUp {
		t.Fatal("PON fault escaped its branch")
	}
}
func TestAPILoopbackAndOriginBoundaries(t *testing.T) {
	_, h, _ := fixture(t)
	for _, tc := range []struct {
		host, origin string
		want         int
	}{
		{"127.0.0.1:8787", "", 200}, {"localhost:8787", "http://localhost:8787", 200},
		{"127.0.0.1:8787", "https://unrelated.example", 403}, {"rebind.example:8787", "http://rebind.example:8787", 403},
	} {
		r := httptest.NewRequest("GET", "http://"+tc.host+"/api/v1/health", nil)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("host=%s origin=%s code=%d", tc.host, tc.origin, w.Code)
		}
	}
}
