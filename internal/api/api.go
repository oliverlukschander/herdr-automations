// Package api is the plugin's machine-readable face: the `--json` output the
// Orchestrator app reads over SSH. The contract it implements lives with the
// app (GAI-1075, 20-contract.md); the rules that shape everything here are
// that stdout carries exactly one JSON object, success or not, and that every
// key the contract names is always present, null when there is nothing to say.
// The app throws stderr away and cannot trust the exit code, so nothing it
// needs may live anywhere else.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Schema is what the app checks for compatibility. It changes only when a
// shape changes in a way an older app would misread.
const Schema = 1

// Version is the plugin's version, set by main.
var Version = "dev"

// Head opens every response.
type Head struct {
	Schema  int    `json:"schema"`
	Version string `json:"version"`
	OK      bool   `json:"ok"`
}

func (h *Head) head() *Head { return h }

// Result is any response: something that embeds Head.
type Result interface{ head() *Head }

// Error codes. The app branches on these, never on the message.
const (
	CodeUsage            = "usage"
	CodeNotFound         = "not_found"
	CodeExists           = "exists"
	CodeInvalid          = "invalid"
	CodeChecksFailed     = "checks_failed"
	CodeConflict         = "conflict"
	CodeLocked           = "locked"
	CodeConfigUnreadable = "config_unreadable"
	CodeDaemonNotRunning = "daemon_not_running"
	CodeInternal         = "internal"
)

// Failure is an error the app can act on. Anything else that reaches Emit is
// reported as internal.
type Failure struct {
	Code          string       `json:"code"`
	Message       string       `json:"message"`
	Diagnostics   []Diagnostic `json:"diagnostics"`
	Checks        []Check      `json:"checks"`
	CurrentSHA256 *string      `json:"current_sha256"`
}

func (f *Failure) Error() string { return f.Message }

// Fail builds a Failure with a formatted message.
func Fail(code, format string, args ...any) *Failure {
	return &Failure{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Emit writes res, or the failure in err, as the one object on w, and reports
// whether it was a success so the caller can pick the exit code.
func Emit(w io.Writer, res Result, err error) bool {
	head := Head{Schema: Schema, Version: Version, OK: err == nil}
	var out any
	if err == nil {
		*res.head() = head
		out = res
	} else {
		var f *Failure
		if !errors.As(err, &f) {
			f = &Failure{Code: CodeInternal, Message: err.Error()}
		}
		if f.Diagnostics == nil {
			f.Diagnostics = []Diagnostic{}
		}
		if f.Checks == nil {
			f.Checks = []Check{}
		}
		out = struct {
			Head
			Error *Failure `json:"error"`
		}{head, f}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // names like "[KAN] Mails -> Kaneo" stay readable
	if encErr := enc.Encode(out); encErr != nil {
		fmt.Fprintf(w, `{"schema":%d,"version":%q,"ok":false,"error":{"code":%q,"message":%q,"diagnostics":[],"checks":[],"current_sha256":null}}`+"\n",
			Schema, Version, CodeInternal, encErr.Error())
		return false
	}
	return err == nil
}

// Time is a moment as the contract writes it: RFC3339 at second precision in
// the machine's zone, or null. Swift's .iso8601 refuses fractional seconds,
// and a mix of both in one response is worse than either.
type Time struct{ time.Time }

func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(t.Local().Format(time.RFC3339))
}

// str is s, or null when there is nothing in it.
func str(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Diagnostic is an entry that did not load, as error.diagnostics carries it.
type Diagnostic struct {
	Name    *string `json:"name"`
	Line    int     `json:"line"`
	Message string  `json:"message"`
}

// Check is one thing a machine needs before it can run an automation.
type Check struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Level   string `json:"level"`
	Message string `json:"message"`
}
