package app

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type helperLaunch struct {
	mu      sync.Mutex
	pending bool
	err     string
}

type helperLaunchStatus struct {
	Available bool   `json:"available"`
	Starting  bool   `json:"starting"`
	Error     string `json:"error,omitempty"`
}

func helperExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if strings.Contains(exe, "/go-build") || strings.Contains(exe, "/go-tool") {
		exe, err = filepath.Abs("bin/ftthlab")
		if err != nil {
			return "", err
		}
	}
	info, err := os.Stat(exe)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", fmt.Errorf("build bin/ftthlab before starting the network helper")
	}
	return exe, nil
}

func helperLauncherAvailable() bool {
	_, pkexecErr := exec.LookPath("pkexec")
	_, binaryErr := helperExecutable()
	return pkexecErr == nil && binaryErr == nil && (os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "")
}

func (a *App) helperLaunchStatus(online bool) helperLaunchStatus {
	a.helperLaunch.mu.Lock()
	defer a.helperLaunch.mu.Unlock()
	status := helperLaunchStatus{Available: helperLauncherAvailable(), Starting: a.helperLaunch.pending && !online}
	if !online {
		status.Error = a.helperLaunch.err
	}
	return status
}

// The browser cannot supply a command, UID, or directory. Only this binary's
// netd subcommand is launched, through the desktop's normal Polkit prompt.
func (a *App) startHelper(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if !decode(w, r, &input) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.Helper.Call(ctx, "GET", "/health", nil, nil); err == nil {
		write(w, http.StatusOK, map[string]bool{"helperOnline": true})
		return
	}
	if !helperLauncherAvailable() {
		fail(w, http.StatusServiceUnavailable, fmt.Errorf("desktop authorization is unavailable; run the helper command shown in Runtime settings in a terminal"))
		return
	}
	exe, err := helperExecutable()
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err)
		return
	}
	a.helperLaunch.mu.Lock()
	defer a.helperLaunch.mu.Unlock()
	if a.helperLaunch.pending {
		write(w, http.StatusAccepted, map[string]bool{"starting": true})
		return
	}
	cmd := exec.Command("pkexec", "--disable-internal-agent", exe, "netd", "--data-dir", a.Dir, "--socket", a.Helper.Socket, "--uid", strconv.Itoa(os.Getuid()))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	output := &helperOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	if err = cmd.Start(); err != nil {
		fail(w, http.StatusServiceUnavailable, fmt.Errorf("start system authorization: %w", err))
		return
	}
	a.helperLaunch.pending = true
	a.helperLaunch.err = ""
	go func() {
		err := cmd.Wait()
		a.helperLaunch.mu.Lock()
		defer a.helperLaunch.mu.Unlock()
		a.helperLaunch.pending = false
		if err == nil {
			a.helperLaunch.err = "Network helper stopped. Start it again to run a lab."
		} else if cmd.ProcessState.ExitCode() == 126 {
			a.helperLaunch.err = "System authorization was cancelled. Start the helper again when ready."
		} else {
			a.helperLaunch.err = fmt.Sprintf("Network helper could not start: %v. %s", err, strings.TrimSpace(output.String()))
		}
	}()
	write(w, http.StatusAccepted, map[string]bool{"starting": true})
}

type helperOutput struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *helperOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if remaining := 8192 - b.b.Len(); remaining > 0 {
		b.b.Write(p[:min(remaining, n)])
	}
	return n, nil
}

func (b *helperOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
