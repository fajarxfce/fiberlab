package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultDataDirKeepsDesktopAndTerminalInSameWorkspace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace with spaces")
	if err := os.MkdirAll(filepath.Join(root, "bin", ".data"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ftthlab\n\ngo 1.26.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Reproduce an accidental second database from a previous double-click.
	if err := os.WriteFile(filepath.Join(root, "bin", ".data", "lab.db"), []byte("legacy"), 0600); err != nil {
		t.Fatal(err)
	}
	dataHome := filepath.Join(t.TempDir(), "data")
	for _, tc := range []struct{ name, executable, cwd, want string }{
		{"terminal", filepath.Join(root, "bin", "ftthlab"), root, filepath.Join(root, ".data")},
		{"double click", filepath.Join(root, "bin", "ftthlab"), filepath.Join(root, "bin"), filepath.Join(root, ".data")},
		{"different cwd", filepath.Join(root, "bin", "ftthlab"), t.TempDir(), filepath.Join(root, ".data")},
		{"go run", "/tmp/go-build123/ftthlab", root, filepath.Join(root, ".data")},
		{"installed binary", "/usr/bin/ftthlab", t.TempDir(), filepath.Join(dataHome, "fiberlab")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultDataDir(tc.executable, tc.cwd, dataHome); got != tc.want {
				t.Fatalf("data directory %q; want %q", got, tc.want)
			}
		})
	}
	if b, err := os.ReadFile(filepath.Join(root, "bin", ".data", "lab.db")); err != nil || string(b) != "legacy" {
		t.Fatal("resolving a data directory modified the other database")
	}
}

func TestDesktopOnlyReusesItsOwnRunningInstance(t *testing.T) {
	for _, tc := range []struct {
		name, appName, directory string
		want                     bool
	}{
		{"same workspace", "Fiberlab", "/workspace/.data", true},
		{"other workspace", "Fiberlab", "/workspace/bin/.data", false},
		{"other service", "Unrelated", "/workspace/.data", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/health" {
					fmt.Fprintf(w, `{"name":%q}`, tc.appName)
				} else {
					fmt.Fprintf(w, `{"dataDir":%q}`, tc.directory)
				}
			}))
			defer server.Close()
			if got := sameInstance(strings.TrimPrefix(server.URL, "http://"), "/workspace/.data"); got != tc.want {
				t.Fatalf("reused instance=%v; want %v", got, tc.want)
			}
		})
	}
}
