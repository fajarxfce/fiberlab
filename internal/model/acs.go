package model

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ACS belongs to the application management plane. It never grants an ACS
// access to the privileged network helper or to arbitrary host commands.
type ACSConfig struct {
	Enabled                   bool   `json:"enabled"`
	URL                       string `json:"url"`
	Username                  string `json:"username"`
	Password                  string `json:"password"`
	PeriodicInformSeconds     int    `json:"periodicInformSeconds"`
	ConnectionRequestListen   string `json:"connectionRequestListen"`
	ConnectionRequestURL      string `json:"connectionRequestUrl"`
	ConnectionRequestUsername string `json:"connectionRequestUsername"`
	ConnectionRequestPassword string `json:"connectionRequestPassword"`
}

type ONUACSConfig struct {
	Disabled              bool   `json:"disabled"`
	URL                   string `json:"url,omitempty"`
	Username              string `json:"username,omitempty"`
	Password              string `json:"password,omitempty"`
	PeriodicInformSeconds int    `json:"periodicInformSeconds,omitempty"`
}

type ACSStatus struct {
	Status               string     `json:"status"`
	DeviceID             string     `json:"deviceId,omitempty"`
	ConnectionRequestURL string     `json:"connectionRequestUrl,omitempty"`
	LastInform           *time.Time `json:"lastInform,omitempty"`
	NextInform           *time.Time `json:"nextInform,omitempty"`
	InformCount          uint64     `json:"informCount"`
	LastError            string     `json:"lastError,omitempty"`
}

func DefaultACS() ACSConfig {
	return ACSConfig{URL: "http://127.0.0.1:7547", PeriodicInformSeconds: 60,
		ConnectionRequestListen: "127.0.0.1:7548", ConnectionRequestURL: "http://127.0.0.1:7548",
		ConnectionRequestUsername: "fiberlab", ConnectionRequestPassword: Secret()}
}

func ValidateACSURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(value) > 2048 || hasControl(value) {
		return fmt.Errorf("ACS URL must be an http(s) URL without embedded credentials or a fragment")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("ACS URL has an invalid port")
		}
	}
	return nil
}

func validateACSCredentials(user, password string) error {
	if len(user) > 256 || len(password) > 256 || hasControl(user) || hasControl(password) {
		return fmt.Errorf("ACS credentials must be at most 256 characters without control characters")
	}
	return nil
}

func ValidateACS(c ACSConfig) error {
	if err := validateACSCredentials(c.Username, c.Password); err != nil {
		return err
	}
	if err := validateACSCredentials(c.ConnectionRequestUsername, c.ConnectionRequestPassword); err != nil {
		return err
	}
	if !c.Enabled {
		return nil
	}
	if err := ValidateACSURL(c.URL); err != nil {
		return err
	}
	if c.PeriodicInformSeconds < 10 || c.PeriodicInformSeconds > 86400 {
		return fmt.Errorf("ACS periodic Inform interval must be 10–86400 seconds")
	}
	host, p, err := net.SplitHostPort(c.ConnectionRequestListen)
	port, portErr := strconv.Atoi(p)
	if err != nil || net.ParseIP(host) == nil || portErr != nil || port < 1024 || port > 65535 {
		return fmt.Errorf("ACS connection-request listener must be an IP:port with port 1024–65535")
	}
	if err := ValidateACSURL(c.ConnectionRequestURL); err != nil {
		return fmt.Errorf("connection-request URL: %w", err)
	}
	u, _ := url.Parse(c.ConnectionRequestURL)
	if u.RawQuery != "" || (net.ParseIP(u.Hostname()) != nil && net.ParseIP(u.Hostname()).IsUnspecified()) {
		return fmt.Errorf("connection-request URL needs a reachable hostname or IP, without a query")
	}
	if strings.TrimSpace(c.ConnectionRequestUsername) == "" || len(c.ConnectionRequestPassword) < 8 {
		return fmt.Errorf("connection-request username and a password of at least 8 characters are required")
	}
	return nil
}

// NetworkLab omits app-owned CWMP configuration from the helper wire format.
// This also keeps app-only upgrades compatible with an already-running netd.
func NetworkLab(l Lab) Lab {
	l.ACS = nil
	l.Nodes = append([]Node(nil), l.Nodes...)
	for i := range l.Nodes {
		l.Nodes[i].Config.ACS = nil
	}
	return l
}
