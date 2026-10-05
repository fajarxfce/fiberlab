package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"ftthlab/internal/model"
	"golang.org/x/sys/unix"
)

func ServeDaemon(ctx context.Context, socket string, e *Engine) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("netd requires root to create TAP devices and PPP network namespaces")
	}
	if err := e.PrepareDirectory(); err != nil {
		return err
	}
	release, err := e.lockRuntime()
	if err != nil {
		return err
	}
	defer release()
	if conn, err := net.DialTimeout("unix", socket, time.Second); err == nil {
		conn.Close()
		return fmt.Errorf("a network helper is already listening on %s", socket)
	}
	if err := e.Recover(ctx); err != nil {
		return fmt.Errorf("recover stale runtime: %w", err)
	}
	var limit unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_NOFILE, &limit) == nil && limit.Cur < 8192 {
		limit.Cur = min(uint64(8192), limit.Max)
		_ = unix.Setrlimit(unix.RLIMIT_NOFILE, &limit)
	}
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket path %s", socket)
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socket)
	if err = os.Chown(socket, e.UID, e.GID); err != nil {
		return err
	}
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(w http.ResponseWriter, err error) { write(w, 400, map[string]string{"error": err.Error()}) }
	decode := func(w http.ResponseWriter, r *http.Request, v any) bool {
		defer r.Body.Close()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(v); err != nil {
			fail(w, err)
			return false
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			fail(w, fmt.Errorf("request must contain one JSON value"))
			return false
		}
		return true
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"ok": true, "checks": Doctor(), "uid": e.UID})
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) { write(w, 200, e.Snapshot()) })
	mux.HandleFunc("POST /start", func(w http.ResponseWriter, r *http.Request) {
		var spec StartSpec
		if !decode(w, r, &spec) {
			return
		}
		if len(spec.Image.SHA256) != 64 {
			fail(w, fmt.Errorf("image needs a recorded SHA-256 digest"))
			return
		}
		if err := e.Start(spec); err != nil {
			fail(w, err)
			return
		}
		write(w, 202, e.Snapshot())
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		if err := e.Stop(r.Context()); err != nil {
			fail(w, err)
			return
		}
		write(w, 200, e.Snapshot())
	})
	mux.HandleFunc("POST /apply", func(w http.ResponseWriter, r *http.Request) {
		var l model.Lab
		if !decode(w, r, &l) {
			return
		}
		if err := e.Apply(r.Context(), l); err != nil {
			fail(w, err)
			return
		}
		write(w, 200, e.Snapshot())
	})
	mux.HandleFunc("POST /exec", func(w http.ResponseWriter, r *http.Request) {
		var spec ExecSpec
		if !decode(w, r, &spec) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		output, err := e.Exec(ctx, spec)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, map[string]string{"output": output})
	})
	mux.HandleFunc("POST /capture", func(w http.ResponseWriter, r *http.Request) {
		var spec CaptureSpec
		if !decode(w, r, &spec) {
			return
		}
		b, err := e.Capture(r.Context(), spec)
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.tcpdump.pcap")
		w.Write(b)
	})
	server := http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		stopCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		_ = e.Stop(stopCtx)
		_ = server.Shutdown(stopCtx)
	}()
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
