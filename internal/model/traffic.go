package model

import (
	"fmt"
	"time"
)

const MaxTrafficKbps = 100_000

// Rates are payload kilobits/second, per ONU, from the subscriber's perspective.
// A zero duration runs until stopped; jobs are runtime state, not lab documents.
type TrafficConfig struct {
	ONUIDs          []string `json:"onuIds"`
	DownloadKbps    int      `json:"downloadKbps"`
	UploadKbps      int      `json:"uploadKbps"`
	DurationSeconds int      `json:"durationSeconds"`
}

func (c TrafficConfig) Validate() error {
	if len(c.ONUIDs) == 0 || len(c.ONUIDs) > MaxONUs {
		return fmt.Errorf("select between 1 and %d ONUs", MaxONUs)
	}
	seen := make(map[string]bool, len(c.ONUIDs))
	for _, id := range c.ONUIDs {
		if id == "" || seen[id] {
			return fmt.Errorf("ONU identifiers must be nonempty and unique")
		}
		seen[id] = true
	}
	if c.DownloadKbps < 0 || c.UploadKbps < 0 || c.DownloadKbps > MaxTrafficKbps || c.UploadKbps > MaxTrafficKbps || c.DownloadKbps+c.UploadKbps == 0 {
		return fmt.Errorf("set a positive download or upload rate; each rate must be between 0 and %d Kbps", MaxTrafficKbps)
	}
	if (c.DownloadKbps+c.UploadKbps)*len(c.ONUIDs) > MaxTrafficKbps {
		return fmt.Errorf("combined target traffic across all selected ONUs must not exceed %d Kbps", MaxTrafficKbps)
	}
	if c.DurationSeconds < 0 || c.DurationSeconds > 86400 {
		return fmt.Errorf("duration must be between 1 and 86400 seconds, or 0 to run until stopped")
	}
	return nil
}

type TrafficONU struct {
	ONUID string `json:"onuId"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type TrafficView struct {
	State     string        `json:"state"`
	Config    TrafficConfig `json:"config"`
	StartedAt time.Time     `json:"startedAt"`
	EndsAt    *time.Time    `json:"endsAt,omitempty"`
	StoppedAt *time.Time    `json:"stoppedAt,omitempty"`
	ONUs      []TrafficONU  `json:"onus"`
}
