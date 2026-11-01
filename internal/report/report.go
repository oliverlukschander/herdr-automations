// Package report records the human-action verdict separately from agent lifecycle state.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Report struct {
	UserActionRequired *bool  `json:"user_action_required"`
	Summary            string `json:"summary"`
}

func (r Report) Validate() error {
	if r.UserActionRequired == nil || strings.TrimSpace(r.Summary) == "" || len(r.Summary) > 4000 {
		return fmt.Errorf("report requires an explicit user_action_required boolean and a summary (1–4000 bytes)")
	}
	return nil
}

func Read(path string) (Report, error) {
	var r Report
	f, err := os.Open(path)
	if err != nil {
		return r, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return r, err
	}
	if info.Size() > 8192 {
		return r, fmt.Errorf("report is too large")
	}
	decoder := json.NewDecoder(f)
	if err := decoder.Decode(&r); err != nil {
		return r, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return r, fmt.Errorf("report contains trailing data")
	}
	return r, r.Validate()
}

func Write(path string, r Report) error {
	if err := r.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".report-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
