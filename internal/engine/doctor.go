package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

type Check struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Required bool   `json:"required"`
	Detail   string `json:"detail"`
}

func Doctor() []Check {
	checks := []Check{{Name: "Linux x86_64", OK: runtime.GOOS == "linux" && runtime.GOARCH == "amd64", Required: true, Detail: runtime.GOOS + "/" + runtime.GOARCH}}
	for _, name := range []string{"qemu-system-x86_64", "qemu-img", "ip", "bridge", "pppd", "ping"} {
		path, err := exec.LookPath(name)
		if err != nil {
			path = "Not installed"
		}
		checks = append(checks, Check{name, err == nil, true, path})
	}
	for _, path := range []string{"/dev/kvm", "/dev/net/tun", "/dev/ppp"} {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		detail := "Accessible"
		if err != nil {
			detail = err.Error()
		} else {
			f.Close()
		}
		checks = append(checks, Check{path, err == nil, true, detail})
	}
	plugin := PPPPlugin()
	checks = append(checks, Check{"PPPoE plugin", plugin != "", true, plugin})
	checks = append(checks, Check{"Network privileges", os.Geteuid() == 0, true, "Network namespace, TAP and PPP setup runs in the root netd helper"})
	return checks
}
func PPPPlugin() string {
	for _, base := range []string{"/usr/lib/pppd", "/usr/lib64/pppd", "/usr/lib/x86_64-linux-gnu/pppd"} {
		matches, _ := filepath.Glob(base + "/*/pppoe.so")
		if len(matches) > 0 {
			return matches[len(matches)-1]
		}
		matches, _ = filepath.Glob(base + "/*/rp-pppoe.so")
		if len(matches) > 0 {
			return matches[len(matches)-1]
		}
	}
	return ""
}
func RequireRuntime() error {
	for _, c := range Doctor() {
		if c.Required && !c.OK {
			return fmt.Errorf("runtime prerequisite %s: %s", c.Name, c.Detail)
		}
	}
	return nil
}
