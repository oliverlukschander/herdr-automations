package main

import (
	"slices"
	"testing"
)

// Review finding 10: the scheduler runs only for a bare `daemon`. Anything
// else is `daemon start` (wherever "start" sits) or a usage error, never a
// scheduler started inside the SSH session asking.
func TestDaemonRunsOnlyWhenAskedWithNothingElse(t *testing.T) {
	cases := []struct {
		args []string
		mode string
		rest []string
	}{
		{nil, "run", nil},
		{[]string{"start"}, "start", []string{}},
		{[]string{"start", "--json"}, "start", []string{"--json"}},
		{[]string{"--json", "start"}, "start", []string{"--json"}},
		{[]string{"--json"}, "usage", nil},
		{[]string{"restart"}, "usage", nil},
	}
	for _, c := range cases {
		mode, rest := daemonMode(c.args)
		if mode != c.mode || (c.rest != nil && !slices.Equal(rest, c.rest)) {
			t.Errorf("daemonMode(%q) = %s %q, want %s %q", c.args, mode, rest, c.mode, c.rest)
		}
	}
}
