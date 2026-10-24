package config

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// This file is the text side of automations.yaml: which lines belong to which
// entry. Save re-marshals the whole file, which drops every comment and blank
// line in it and writes ~ out as an absolute path. A writer that knows where
// an entry starts and ends can replace that entry and leave every other byte
// alone — and a hand-written file is mostly comments worth keeping.

// locate works out the lines each item occupies.
//
// An entry starts at its dash, or at the comments directly above it at the
// dash's own indentation — a heading belongs to what it heads. It ends at its
// last line indented past the dash, so the blank lines between entries and a
// comment block trailing the list belong to no entry and are never touched.
func (f *File) locate(seq *yaml.Node) {
	if len(f.Items) == 0 {
		return
	}
	if seq.Style&yaml.FlowStyle != 0 {
		for i := range f.Items {
			f.Items[i].first, f.Items[i].last = -1, -1
		}
		return
	}
	lines := splitLines(f.Raw)
	dash := seq.Column - 1
	f.dash = dash

	starts := make([]int, len(f.Items))
	for i, it := range f.Items {
		s := dashLine(lines, it.node.Line-1, dash)
		starts[i] = s
		first := s
		for first > 0 && isComment(lines[first-1]) && indent(lines[first-1]) == dash {
			first--
		}
		f.Items[i].first = first
	}
	end := sequenceEnd(lines, starts[len(starts)-1], dash)
	for i := range f.Items {
		bound := end
		if i+1 < len(f.Items) {
			bound = f.Items[i+1].first
		}
		last := starts[i]
		for l := starts[i] + 1; l < bound; l++ {
			if !blank(lines[l]) && indent(lines[l]) > dash {
				last = l
			}
		}
		f.Items[i].last = last
	}
}

// dashLine finds the line holding an item's dash: the line of its first key,
// or the one above when the dash stands alone ("-" then the mapping below).
func dashLine(lines []string, keyLine, dash int) int {
	if !hasDash(lines[keyLine], dash) && keyLine > 0 && hasDash(lines[keyLine-1], dash) {
		return keyLine - 1
	}
	return keyLine
}

// sequenceEnd is the first line after the last item's dash that is outside the
// sequence: content back at or left of the dash. Comments do not end it — an
// entry may carry one at any indentation — they are only left out of its span
// when nothing of the entry follows them.
func sequenceEnd(lines []string, lastStart, dash int) int {
	for l := lastStart + 1; l < len(lines); l++ {
		if !blank(lines[l]) && !isComment(lines[l]) && indent(lines[l]) <= dash {
			return l
		}
	}
	return len(lines)
}

// Text is the entry as written, its own comments included, de-indented into a
// mapping of its own: what `show` hands out and `apply` takes back.
func (f *File) Text(it Item) string {
	if it.first < 0 {
		out, _ := yaml.Marshal(it.node)
		return string(out)
	}
	lines := splitLines(f.Raw)
	key := it.node.Column - 1
	start := dashLine(lines, it.node.Line-1, f.dash)
	var b strings.Builder
	for l := it.first; l <= it.last; l++ {
		line := lines[l]
		if l == start {
			line = line[:f.dash] + " " + line[f.dash+1:]
			if blank(line) {
				continue // a dash on its own line
			}
		}
		b.WriteString(line[min(key, indent(line)):])
		b.WriteByte('\n')
	}
	return b.String()
}

// ItemAt returns the entry whose first key is on line, or nil.
func (f *File) ItemAt(line int) *Item {
	for i := range f.Items {
		if f.Items[i].Line == line {
			return &f.Items[i]
		}
	}
	return nil
}

func splitLines(raw []byte) []string { return strings.Split(string(raw), "\n") }

func hasDash(line string, col int) bool {
	if len(line) <= col || line[col] != '-' || indent(line) != col {
		return false
	}
	return len(line) == col+1 || line[col+1] == ' ' || line[col+1] == '\r'
}

func indent(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

func blank(line string) bool { return strings.TrimSpace(line) == "" }

func isComment(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "#") }
