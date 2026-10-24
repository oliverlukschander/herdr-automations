package api

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"time"

	"github.com/DnzzL/herdr-automations/internal/config"
	"github.com/DnzzL/herdr-automations/internal/daemon"
	"github.com/DnzzL/herdr-automations/internal/history"
)

// ListResult is `list --json`.
type ListResult struct {
	Head
	Machine     Machine      `json:"machine"`
	Config      ConfigInfo   `json:"config"`
	Daemon      Daemon       `json:"daemon"`
	Automations []Automation `json:"automations"`
	Invalid     []Invalid    `json:"invalid"`
}

// Machine says which machine answered and what its clock reads, so the app
// can place next runs and spot a skewed clock.
type Machine struct {
	Hostname string  `json:"hostname"`
	TZ       *string `json:"tz"`
	Now      Time    `json:"now"`
}

// ConfigInfo names the file and the hash a write has to match (--if-match).
// A missing file hashes as empty, so creating the first entry needs no
// special case.
type ConfigInfo struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	SHA256 string `json:"sha256"`
}

// Daemon is whether the scheduler is running here.
type Daemon struct {
	Running bool `json:"running"`
	PID     *int `json:"pid"`
	Since   Time `json:"since"`
}

func configInfo(f *config.File) ConfigInfo {
	sum := sha256.Sum256(f.Raw)
	return ConfigInfo{Path: config.Path(), Exists: f.Exists, SHA256: hex.EncodeToString(sum[:])}
}

// readConfig reads automations.yaml, turning "cannot read it at all" into the
// failure the app expects for it.
func readConfig() (*config.File, error) {
	f, err := config.Read()
	if err != nil {
		return nil, Fail(CodeConfigUnreadable, "%v", err)
	}
	return f, nil
}

// List answers `list --json`.
func List(now time.Time) (*ListResult, error) {
	f, err := readConfig()
	if err != nil {
		return nil, err
	}
	latest, err := history.Latest()
	if err != nil {
		return nil, err
	}

	res := &ListResult{
		Machine:     machine(now),
		Config:      configInfo(f),
		Daemon:      daemonOf(daemon.Probe()),
		Automations: []Automation{},
		Invalid:     []Invalid{},
	}
	for _, a := range f.Automations {
		var last *history.Record
		if r, ok := latest[a.Name]; ok {
			last = &r
		}
		v, err := automationOf(f, a, last, now)
		if err != nil {
			return nil, err
		}
		res.Automations = append(res.Automations, v)
	}
	for _, d := range f.Invalid {
		res.Invalid = append(res.Invalid, invalidOf(f, d))
	}
	return res, nil
}

func daemonOf(st daemon.Status) Daemon {
	d := Daemon{Running: st.Running}
	if st.Running {
		if st.PID > 0 {
			d.PID = &st.PID
		}
		d.Since = Time{st.Since}
	}
	return d
}

func machine(now time.Time) Machine {
	host, _ := os.Hostname()
	return Machine{Hostname: host, TZ: str(zoneName()), Now: Time{now}}
}

// zoneName is the machine's IANA zone, best effort: TZ when set, else where
// /etc/localtime points (…/zoneinfo/Europe/Vienna on macOS and Linux alike).
// Go's own time.Local only knows it as "Local".
func zoneName() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		return tz
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, zone, ok := strings.Cut(target, "zoneinfo/"); ok {
			return zone
		}
	}
	return ""
}
