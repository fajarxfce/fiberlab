package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func dataDirDefault() string {
	executable, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	workingDir, _ := os.Getwd()
	dataHome := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(dataHome) {
		userDir, _ := os.UserHomeDir()
		dataHome = filepath.Join(userDir, ".local", "share")
	}
	return defaultDataDir(executable, workingDir, dataHome)
}

func defaultDataDir(executable, workingDir, dataHome string) string {
	// A repository build uses the same workspace from a terminal or a file
	// manager. In particular, bin/.data must not shadow the project database.
	base := filepath.Dir(executable)
	if filepath.Base(base) == "bin" {
		base = filepath.Dir(base)
	}
	for _, candidate := range []string{base, workingDir} {
		b, err := os.ReadFile(filepath.Join(candidate, "go.mod"))
		fields := strings.Fields(string(b))
		if err == nil && len(fields) >= 2 && fields[0] == "module" && fields[1] == "ftthlab" {
			return filepath.Join(candidate, ".data")
		}
	}
	return filepath.Join(dataHome, "fiberlab")
}

func openBrowser(address string) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return
	}
	cmd := exec.Command("xdg-open", "http://"+address)
	if err := cmd.Start(); err != nil {
		log.Printf("Open http://%s in your browser: %v", address, err)
		return
	}
	go func() { _ = cmd.Wait() }()
}

func sameInstance(address, dataDir string) bool {
	client := http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Get("http://" + address + "/api/v1/health")
	if err != nil {
		return false
	}
	var health struct{ Name string }
	err = json.NewDecoder(response.Body).Decode(&health)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || health.Name != "Fiberlab" {
		return false
	}
	response, err = client.Get("http://" + address + "/api/v1/system")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	var system struct{ DataDir string }
	return response.StatusCode == http.StatusOK && json.NewDecoder(response.Body).Decode(&system) == nil && filepath.Clean(system.DataDir) == filepath.Clean(dataDir)
}
