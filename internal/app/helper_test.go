package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"ftthlab/internal/model"
)

func TestDesktopHelperLaunchIsFixedDeduplicatedAndRetryable(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "ftthlab"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	release := filepath.Join(dir, "release")
	arguments := filepath.Join(dir, "arguments")
	launches := filepath.Join(dir, "launches")
	stub := "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$FIBERLAB_TEST_ARGS\"\nprintf x >> \"$FIBERLAB_TEST_LAUNCHES\"\nwhile [ ! -e \"$FIBERLAB_TEST_RELEASE\" ]; do /usr/bin/sleep 0.02; done\nexit 126\n"
	if err := os.WriteFile(filepath.Join(dir, "pkexec"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("DISPLAY", ":test")
	t.Setenv("FIBERLAB_TEST_ARGS", arguments)
	t.Setenv("FIBERLAB_TEST_RELEASE", release)
	t.Setenv("FIBERLAB_TEST_LAUNCHES", launches)
	a, err := New(filepath.Join(dir, "workspace with spaces"), "127.0.0.1:8787", filepath.Join(dir, "custom helper.sock"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	h := a.Handler()
	path := "/api/v1/system/helper/start"
	if w := request(t, h, "POST", path, map[string]any{"command": "/bin/sh", "uid": 0}); w.Code != 400 {
		t.Fatalf("accepted browser-supplied command: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:8787"+path, strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://unrelated.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-origin helper launch: %d", w.Code)
	}
	if _, err := os.Stat(arguments); !os.IsNotExist(err) {
		t.Fatal("rejected request launched a process")
	}
	for i := 0; i < 2; i++ {
		if w := request(t, h, "POST", path, map[string]any{}); w.Code != 202 {
			t.Fatalf("helper launch: %d %s", w.Code, w.Body.String())
		}
	}
	waitFor := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !check() {
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for helper launch state")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor(func() bool { b, _ := os.ReadFile(launches); return len(b) > 0 })
	b, _ := os.ReadFile(arguments)
	executable, _ := helperExecutable()
	want := []string{"--disable-internal-agent", executable, "netd", "--data-dir", a.Dir, "--socket", a.Helper.Socket, "--uid", strconv.Itoa(os.Getuid())}
	if got := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00"); !reflect.DeepEqual(got, want) {
		t.Fatalf("helper arguments %q; want %q", got, want)
	}
	if b, _ := os.ReadFile(launches); string(b) != "x" {
		t.Fatal("duplicate helper process launched")
	}
	if !a.helperLaunchStatus(false).Starting {
		t.Fatal("pending system authorization was not reported")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(func() bool { return !a.helperLaunchStatus(false).Starting })
	if !strings.Contains(a.helperLaunchStatus(false).Error, "cancelled") {
		t.Fatal("cancelled system prompt was not explained")
	}
	if w := request(t, h, "POST", path, map[string]any{}); w.Code != 202 {
		t.Fatalf("retry after cancellation: %d", w.Code)
	}
	waitFor(func() bool { return !a.helperLaunchStatus(false).Starting })
	if b, _ := os.ReadFile(launches); string(b) != "xx" {
		t.Fatal("retry did not launch a new system prompt")
	}
}

func TestConnectionsIdentifyTheActiveLabAndWinboxCredentials(t *testing.T) {
	a, h, selected := fixture(t)
	active := model.Preset(1)
	active.Name = "Another running lab"
	if err := a.Store.Create(context.Background(), active); err != nil {
		t.Fatal(err)
	}
	a.viewMu.Lock()
	a.view = model.RuntimeView{LabID: active.ID, Phase: "running"}
	a.viewMu.Unlock()
	w := request(t, h, "GET", "/api/v1/labs/"+selected.ID+"/connections", nil)
	var response struct {
		ActiveLab struct{ ID, Name string }
		Devices   []struct {
			Kind, Username, Password string
			Services                 map[string]struct{ Port int }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ActiveLab.ID != active.ID || response.ActiveLab.Name != active.Name {
		t.Fatal("connection details concealed a different active lab")
	}
	for _, device := range response.Devices {
		if device.Kind != "router" {
			continue
		}
		if device.Services["winbox"].Port != 8291 || device.Username != "admin" || device.Password != selected.Nodes[0].Config.Password {
			t.Fatal("Winbox endpoint or selected-lab credentials are incorrect")
		}
		return
	}
	t.Fatal("router connection was missing")
}
