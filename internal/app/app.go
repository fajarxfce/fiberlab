package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ftthlab/internal/cwmp"
	"ftthlab/internal/engine"
	"ftthlab/internal/images"
	"ftthlab/internal/model"
	"ftthlab/internal/protocol"
	"ftthlab/internal/site"
	"ftthlab/internal/store"
)

type ImageJob struct {
	Status   string `json:"status"`
	Version  string `json:"version"`
	Received int64  `json:"received"`
	Total    int64  `json:"total"`
	Error    string `json:"error,omitempty"`
}
type App struct {
	Store        *store.Store
	Images       *images.Manager
	Helper       *engine.Client
	ACS          *cwmp.Manager
	Dir, Address string
	Dev          bool
	mu           sync.Mutex
	viewMu       sync.Mutex
	view         model.RuntimeView
	seenRun      string
	seenSeq      int64
	watchers     map[chan struct{}]bool
	controlToken string
	imageJob     ImageJob
	ctx          context.Context
	cancel       context.CancelFunc
	pollDone     chan struct{}
}

func New(dir, address, socket string, dev bool) (*App, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{Store: db, Images: &images.Manager{Dir: filepath.Join(dir, "images")}, Helper: engine.NewClient(socket), Dir: dir, Address: address, Dev: dev, watchers: map[chan struct{}]bool{}, ctx: ctx, cancel: cancel, pollDone: make(chan struct{}), imageJob: ImageJob{Status: "idle"}}
	tokenPath := filepath.Join(dir, "control.key")
	b, err := os.ReadFile(tokenPath)
	if os.IsNotExist(err) {
		b = []byte(model.Secret())
		err = os.WriteFile(tokenPath, b, 0600)
	}
	if err != nil {
		db.Close()
		cancel()
		return nil, err
	}
	a.controlToken = string(b)
	labs, err := db.List(ctx)
	if err != nil {
		db.Close()
		cancel()
		return nil, err
	}
	if len(labs) == 0 {
		l := model.Preset(8)
		if err = db.Create(ctx, l); err != nil {
			db.Close()
			cancel()
			return nil, err
		}
		a.event(l.ID, "lab", "info", "Workspace", "Lab created. All devices are stopped until a real runtime is started.")
	}
	a.ACS = cwmp.New(ctx, cwmp.Hooks{Load: db.LoadCWMP, Save: db.SaveCWMP, Reboot: a.rebootFromACS,
		Event: func(labID, level, onuID, message string) { a.event(labID, "acs", level, onuID, message) }})
	go a.poll()
	return a, nil
}
func (a *App) Close() { a.cancel(); <-a.pollDone; a.ACS.Close(); a.Store.Close() }
func (a *App) event(id, kind, level, subject, message string) {
	_, _ = a.Store.AddEvent(a.ctx, model.Event{LabID: id, At: time.Now().UTC(), Kind: kind, Level: level, Subject: subject, Message: message})
	a.notify()
}
func (a *App) notify() {
	a.viewMu.Lock()
	defer a.viewMu.Unlock()
	for ch := range a.watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
func (a *App) poll() {
	defer close(a.pollDone)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
		v, err := a.Helper.Status(ctx)
		cancel()
		if err == nil {
			a.viewMu.Lock()
			a.view = v
			runChanged := a.seenRun != v.RunID
			if runChanged {
				a.seenRun = v.RunID
				a.seenSeq = 0
			}
			last := a.seenSeq
			a.viewMu.Unlock()
			for _, event := range v.Events {
				if event.Seq > last {
					workerSeq := event.Seq
					event.Seq = 0
					_, _ = a.Store.AddEvent(a.ctx, event)
					a.viewMu.Lock()
					a.seenSeq = workerSeq
					a.viewMu.Unlock()
				}
			}
		} else {
			a.viewMu.Lock()
			if a.view.Phase != "stopped" && a.view.Phase != "" && a.view.Phase != "error" {
				a.view.Phase = "unavailable"
				a.view.Message = "Network helper disconnected; live device and session state is unknown."
				for i := range a.view.Sessions {
					a.view.Sessions[i].Status = "unknown"
				}
				for id, n := range a.view.Nodes {
					n.Status = "unknown"
					n.Reason = "Network helper disconnected"
					a.view.Nodes[id] = n
				}
				a.view.Metrics.ActiveSessions = 0
			}
			a.viewMu.Unlock()
		}
		if err == nil && v.Phase == "running" {
			if lab, loadErr := a.Store.Get(a.ctx, v.LabID); loadErr == nil {
				a.ACS.Sync(lab, v)
			} else {
				a.ACS.Sync(model.Lab{}, model.RuntimeView{})
			}
		} else {
			a.ACS.Sync(model.Lab{}, model.RuntimeView{})
		}
		a.notify()
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (a *App) runtime(l model.Lab) model.RuntimeView {
	a.viewMu.Lock()
	v := a.view
	v.Nodes = make(map[string]model.NodeState, len(a.view.Nodes))
	for id, node := range a.view.Nodes {
		v.Nodes[id] = node
	}
	v.Sessions = append([]model.Session{}, a.view.Sessions...)
	a.viewMu.Unlock()
	if v.LabID != l.ID || v.Phase == "" {
		v = model.Preview(l)
	}
	if v.Phase == "stopped" || v.Phase == "error" {
		v.Nodes = model.Derive(l, false)
		preview := model.Preview(l)
		v.Sessions = preview.Sessions
		v.Metrics.ConfiguredSessions = len(l.Subscribers)
	}
	v.Events = nil
	v.ACS = a.ACS.Snapshot(l.ID)
	v.Metrics.GoRSSBytes = engine.RSS(os.Getpid())
	return v
}
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"ok": true, "name": "Fiberlab", "version": "0.1.0"})
	})
	mux.HandleFunc("GET /api/v1/system", a.system)
	mux.HandleFunc("GET /api/v1/capabilities", func(w http.ResponseWriter, r *http.Request) { write(w, 200, protocol.Capabilities()) })
	mux.HandleFunc("GET /api/v1/labs", func(w http.ResponseWriter, r *http.Request) {
		labs, err := a.Store.List(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		write(w, 200, labs)
	})
	mux.HandleFunc("POST /api/v1/labs", a.createLab)
	mux.HandleFunc("POST /api/v1/labs/import", a.importLab)
	mux.HandleFunc("GET /api/v1/labs/{id}", func(w http.ResponseWriter, r *http.Request) {
		l, err := a.Store.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, 404, err)
			return
		}
		write(w, 200, l)
	})
	mux.HandleFunc("PUT /api/v1/labs/{id}", a.saveLab)
	mux.HandleFunc("DELETE /api/v1/labs/{id}", a.deleteLab)
	mux.HandleFunc("GET /api/v1/labs/{id}/export", a.exportLab)
	mux.HandleFunc("GET /api/v1/labs/{id}/runtime", func(w http.ResponseWriter, r *http.Request) {
		l, err := a.Store.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, 404, err)
			return
		}
		write(w, 200, a.runtime(l))
	})
	mux.HandleFunc("POST /api/v1/labs/{id}/start", a.startLab)
	mux.HandleFunc("POST /api/v1/labs/{id}/stop", a.stopLab)
	mux.HandleFunc("POST /api/v1/labs/{id}/actions", a.action)
	mux.HandleFunc("POST /api/v1/labs/{id}/faults", a.addFault)
	mux.HandleFunc("DELETE /api/v1/labs/{id}/faults/{fault}", a.removeFault)
	mux.HandleFunc("GET /api/v1/labs/{id}/connections", a.connections)
	mux.HandleFunc("GET /api/v1/labs/{id}/events", a.events)
	mux.HandleFunc("GET /api/v1/labs/{id}/stream", a.stream)
	mux.HandleFunc("GET /api/v1/labs/{id}/terminal", a.terminal)
	mux.HandleFunc("POST /api/v1/labs/{id}/exec", a.exec)
	mux.HandleFunc("POST /api/v1/labs/{id}/capture", a.capture)
	mux.HandleFunc("GET /api/v1/images", func(w http.ResponseWriter, r *http.Request) {
		list, err := a.Images.List()
		if err != nil {
			fail(w, 500, err)
			return
		}
		a.viewMu.Lock()
		job := a.imageJob
		a.viewMu.Unlock()
		write(w, 200, map[string]any{"images": list, "job": job, "defaultVersion": images.DefaultVersion})
	})
	mux.HandleFunc("POST /api/v1/images/fetch", a.fetchImage)
	mux.HandleFunc("POST /internal/action", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Fiberlab-Control")), []byte(a.controlToken)) != 1 {
			fail(w, 403, fmt.Errorf("invalid control token"))
			return
		}
		a.viewMu.Lock()
		id := a.view.LabID
		a.viewMu.Unlock()
		r.SetPathValue("id", id)
		a.action(w, r)
	})
	files := site.Files()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/internal/") {
			fail(w, 404, fmt.Errorf("endpoint not found"))
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(files, path); err == nil {
			if strings.HasPrefix(path, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.FileServer(http.FS(files)).ServeHTTP(w, r)
			return
		}
		b, err := fs.ReadFile(files, "index.html")
		if err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(503)
			io.WriteString(w, "Frontend has not been built. Run: cd web && npm install && npm run build\nThen rebuild ftthlab, or open Vite at http://127.0.0.1:5173 for development.")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		if !a.validOrigin(r) {
			fail(w, 403, fmt.Errorf("cross-origin access is not enabled"))
			return
		}
		if r.Method == "POST" || r.Method == "PUT" {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				fail(w, 415, fmt.Errorf("Content-Type must be application/json"))
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *App) validOrigin(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host != "localhost" {
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	return a.Dev && (u.Host == "127.0.0.1:5173" || u.Host == "localhost:5173")
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, err error) {
	write(w, status, map[string]string{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, err)
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		fail(w, 400, fmt.Errorf("request must contain exactly one JSON value"))
		return false
	}
	return true
}
func (a *App) createLab(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
		Empty bool   `json:"empty"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Count < 0 || input.Count > 500 {
		fail(w, 400, fmt.Errorf("choose 1–500 ONUs"))
		return
	}
	l := model.Preset(input.Count)
	if input.Name != "" {
		l.Name = input.Name
	}
	if input.Empty {
		l.Nodes = []model.Node{}
		l.Links = []model.Link{}
		l.Subscribers = []model.Subscriber{}
	}
	list, _ := a.Images.List()
	if len(list) > 0 {
		l.ImageID = list[0].ID
	}
	if err := a.Store.Create(r.Context(), l); err != nil {
		fail(w, 400, err)
		return
	}
	a.event(l.ID, "lab", "info", "Workspace", fmt.Sprintf("Created lab with %d ONUs", len(l.Subscribers)))
	write(w, 201, l)
}
func (a *App) importLab(w http.ResponseWriter, r *http.Request) {
	var l model.Lab
	if !decode(w, r, &l) {
		return
	}
	l.ID = model.NewID("lab-")
	l.Revision = 1
	l.CreatedAt = time.Now().UTC()
	l.UpdatedAt = l.CreatedAt
	if err := a.Store.Create(r.Context(), l); err != nil {
		fail(w, 400, err)
		return
	}
	a.event(l.ID, "lab", "info", "Workspace", "Imported topology; runtime remains stopped")
	write(w, 201, l)
}
func (a *App) saveLab(w http.ResponseWriter, r *http.Request) {
	var l model.Lab
	if !decode(w, r, &l) {
		return
	}
	if l.ID != r.PathValue("id") {
		fail(w, 400, fmt.Errorf("lab ID cannot change"))
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	old, err := a.Store.Get(r.Context(), l.ID)
	if err != nil {
		fail(w, 404, err)
		return
	}
	if old.Revision != l.Revision {
		fail(w, 409, store.ErrConflict)
		return
	}
	saved, err := a.commit(r.Context(), old, l)
	if err != nil {
		status := 400
		if errors.Is(err, store.ErrConflict) {
			status = 409
		}
		fail(w, status, err)
		return
	}
	write(w, 200, saved)
}
func (a *App) commit(ctx context.Context, old, next model.Lab) (model.Lab, error) {
	if err := model.Validate(next); err != nil {
		return next, err
	}
	v := a.runtime(old)
	if v.Phase != "stopped" && v.Phase != "error" {
		if v.Phase != "running" {
			return next, fmt.Errorf("wait for runtime startup/stop to finish before editing")
		}
		if err := a.Helper.Call(ctx, "POST", "/apply", next, nil); err != nil {
			return next, err
		}
	}
	next.CreatedAt = old.CreatedAt
	saved, err := a.Store.Save(ctx, next, old.Revision)
	if err != nil && v.Phase == "running" {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_ = a.Helper.Call(rollbackCtx, "POST", "/apply", old, nil)
		cancel()
	}
	if err == nil {
		a.notify()
	}
	return saved, err
}
func (a *App) deleteLab(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	l, err := a.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	v := a.runtime(l)
	if v.Phase != "stopped" && v.Phase != "error" {
		fail(w, 409, fmt.Errorf("stop the lab before deleting it"))
		return
	}
	if err = a.Store.Delete(r.Context(), l.ID); err != nil {
		fail(w, 500, err)
		return
	}
	write(w, 200, map[string]bool{"ok": true})
}
func (a *App) exportLab(w http.ResponseWriter, r *http.Request) {
	l, err := a.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+l.ID+`.fiberlab.json"`)
	write(w, 200, l)
}
func (a *App) system(w http.ResponseWriter, r *http.Request) {
	var h struct {
		OK     bool           `json:"ok"`
		Checks []engine.Check `json:"checks"`
		UID    int            `json:"uid"`
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	err := a.Helper.Call(ctx, "GET", "/health", nil, &h)
	checks := engine.Doctor()
	if err == nil {
		checks = h.Checks
	}
	exe, _ := os.Executable()
	command := fmt.Sprintf("sudo %s netd --data-dir %s --uid %d", shellQuote(exe), shellQuote(a.Dir), os.Getuid())
	if strings.Contains(exe, "/go-build") || strings.Contains(exe, "/go-tool") {
		command = fmt.Sprintf("sudo ./bin/ftthlab netd --data-dir %s --uid %d", shellQuote(a.Dir), os.Getuid())
	}
	write(w, 200, map[string]any{"helperOnline": err == nil, "checks": checks, "helperCommand": command, "dataDir": a.Dir, "socket": a.Helper.Socket, "stack": []string{"Go", "Svelte 5", "SQLite"}, "network": map[string]string{"management": "10.203.0.0/24", "testOrigin": "198.18.0.1:8080", "subscribers": "172.30.0.0/22"}})
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (a *App) startLab(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	l, err := a.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	image, err := a.Images.Get(l.ImageID)
	if err != nil {
		fail(w, 400, err)
		return
	}
	spec := engine.StartSpec{Lab: l, Image: image, ControlURL: "http://" + a.Address + "/internal/action", ControlToken: a.controlToken}
	var view model.RuntimeView
	if err = a.Helper.Call(r.Context(), "POST", "/start", spec, &view); err != nil {
		a.event(l.ID, "runtime", "error", "Start failed", err.Error())
		fail(w, 503, err)
		return
	}
	a.viewMu.Lock()
	a.view = view
	a.viewMu.Unlock()
	a.event(l.ID, "runtime", "info", "Lab", "Runtime startup requested")
	write(w, 202, view)
}
func (a *App) stopLab(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	l, err := a.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	view := a.runtime(l)
	if view.Phase == "stopped" {
		write(w, 200, view)
		return
	}
	var next model.RuntimeView
	if err = a.Helper.Call(r.Context(), "POST", "/stop", nil, &next); err != nil {
		fail(w, 503, err)
		return
	}
	a.viewMu.Lock()
	a.view = next
	a.viewMu.Unlock()
	a.event(l.ID, "runtime", "info", "Lab", "Stopped network runtime")
	write(w, 200, next)
}
func (a *App) events(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	events, err := a.Store.Events(r.Context(), r.PathValue("id"), after)
	if err != nil {
		fail(w, 500, err)
		return
	}
	write(w, 200, events)
}
func (a *App) stream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := a.Store.Get(r.Context(), id); err != nil {
		fail(w, 404, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, fmt.Errorf("streaming unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := make(chan struct{}, 1)
	a.viewMu.Lock()
	a.watchers[ch] = true
	a.viewMu.Unlock()
	defer func() { a.viewMu.Lock(); delete(a.watchers, ch); a.viewMu.Unlock() }()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	ch <- struct{}{}
	lastEvents := int64(0)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-ch:
			l, err := a.Store.Get(r.Context(), id)
			if err != nil {
				return
			}
			events, _ := a.Store.Events(r.Context(), id, lastEvents)
			for _, e := range events {
				if e.Seq > lastEvents {
					lastEvents = e.Seq
				}
			}
			data, _ := json.Marshal(map[string]any{"runtime": a.runtime(l), "revision": l.Revision, "events": events})
			if _, err = fmt.Fprintf(w, "event: state\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
func (a *App) fetchImage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version string `json:"version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Version == "" {
		input.Version = images.DefaultVersion
	}
	a.viewMu.Lock()
	if a.imageJob.Status == "downloading" {
		a.viewMu.Unlock()
		fail(w, 409, fmt.Errorf("an image download is already running"))
		return
	}
	a.imageJob = ImageJob{Status: "downloading", Version: input.Version}
	a.viewMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
		defer cancel()
		_, err := a.Images.Fetch(ctx, input.Version, func(received, total int64) {
			a.viewMu.Lock()
			a.imageJob.Received = received
			a.imageJob.Total = total
			a.viewMu.Unlock()
		})
		a.viewMu.Lock()
		if err != nil {
			a.imageJob.Status = "error"
			a.imageJob.Error = err.Error()
		} else {
			a.imageJob.Status = "ready"
		}
		a.viewMu.Unlock()
		a.notify()
	}()
	write(w, 202, map[string]string{"status": "downloading"})
}
