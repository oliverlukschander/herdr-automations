package config

import (
	"io"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file is the text side of automations.yaml: which lines belong to which
// entry. Save re-marshalled the whole file, which dropped every comment and
// blank line in it and wrote ~ out as an absolute path. A writer that knows
// where an entry starts and ends can replace that entry and leave every other
// byte alone, and a hand-written file is mostly comments worth keeping.
//
// Nothing here may panic on any input: the daemon loads the file every 30 s,
// and a loader that panics is a scheduler that is gone. Where the text and
// yaml.v3 disagree, the entry is marked as one that cannot be edited here
// (first = -1) rather than guessed at.

// locate works out the lines each item occupies. next is the 0-based line of
// the top-level key after automations:, or the number of lines.
//
// An entry starts at its dash, or at the comments directly above it at the
// dash's own indentation: a heading belongs to what it heads. It runs to the
// next entry's first line (the last one to next), less the blank lines and
// the comments back at the dash that trail it. Those belong to no entry and
// are never touched. A block scalar kept with |+ owns the blank lines after
// it: they are part of its value.
//
// Each span is then checked: its text, read on its own, must be the entry
// yaml.v3 read. One that is not cannot be edited here, but still loads.
func (f *File) locate(seq *yaml.Node, next int) {
	unspliceable := func() {
		for i := range f.Items {
			f.Items[i].first, f.Items[i].last = -1, -1
		}
	}
	if len(f.Items) == 0 {
		return
	}
	if seq.Style&yaml.FlowStyle != 0 || seq.Column < 1 {
		unspliceable()
		return
	}
	lines := f.lines
	dash := seq.Column - 1
	f.dash = dash
	next = min(next, len(lines))
	for l := f.keyLine + 1; l < next; l++ {
		if docMarker(lines[l].text) {
			next = l // the next document is no entry's
			break
		}
	}

	starts := make([]int, len(f.Items))
	for i, it := range f.Items {
		kl := it.node.Line - 1
		if kl <= f.keyLine || kl >= next || (i > 0 && kl <= starts[i-1]) {
			unspliceable()
			return
		}
		s := dashLine(lines, kl, dash)
		lower := f.keyLine + 1
		if i > 0 {
			lower = starts[i-1] + 1
		}
		first := s
		for first-1 >= lower && isComment(lines[first-1].text) && indent(lines[first-1].text) == dash {
			first--
		}
		starts[i], f.Items[i].first = s, first
	}
	for i := range f.Items {
		bound := next
		if i+1 < len(f.Items) {
			bound = f.Items[i+1].first
		}
		last := starts[i]
		for l := bound - 1; l > starts[i]; l-- {
			t := lines[l].text
			if !blank(t) && !(isComment(t) && indent(t) <= dash) {
				last = l
				break
			}
		}
		if keepsTrailing(lines, lastValue(f.Items[i].node)) {
			// Its blank lines, but not what is left after the file's final
			// line break: that ends the file, it adds no line to the value.
			for last+1 < bound && blank(lines[last+1].text) && lines[last+1].brk != "" {
				last++
			}
		}
		f.Items[i].last, f.Items[i].end = last, last
	}
	for i := range f.Items {
		if !f.readsBack(f.Items[i]) {
			f.Items[i].first, f.Items[i].last = -1, -1
		}
	}
}

// readsBack reports whether an item's text, read on its own, is the entry
// yaml.v3 read from the whole file: the check that makes every span safe to
// splice. An entry using an anchor from elsewhere fails it too, rightly.
func (f *File) readsBack(it Item) bool {
	dec := yaml.NewDecoder(strings.NewReader(f.Text(it)))
	var doc yaml.Node
	if dec.Decode(&doc) != nil || len(doc.Content) != 1 || dec.Decode(new(yaml.Node)) != io.EOF {
		return false // not exactly one document
	}
	var alone, whole any
	if doc.Content[0].Decode(&alone) != nil || it.node.Decode(&whole) != nil {
		return false
	}
	return reflect.DeepEqual(alone, whole)
}

// lastValue is the value of a mapping's last key, or nil.
func lastValue(m *yaml.Node) *yaml.Node {
	if m.Kind != yaml.MappingNode || len(m.Content) < 2 {
		return nil
	}
	return m.Content[len(m.Content)-1]
}

// keepsTrailing reports whether v is a block scalar with keep chomping (|+,
// >+), whose value includes the blank lines that follow it. lines are the
// ones v's position counts in.
func keepsTrailing(lines []line, v *yaml.Node) bool {
	return v != nil && v.Line >= 1 && v.Line <= len(lines) && keptBlock(lines[v.Line-1].text, v)
}

// keptBlock is keepsTrailing given the line v's header is on.
func keptBlock(t string, v *yaml.Node) bool {
	if v.Kind != yaml.ScalarNode || v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0 {
		return false
	}
	header := t[byteAt(t, v.Column-1):]
	if end := strings.IndexAny(header, " \t#"); end >= 0 {
		header = header[:end]
	}
	return strings.Contains(header, "+")
}

// dashLine finds the line holding an item's dash: the line of its first key,
// or the one above when the dash stands alone ("-" then the mapping below).
func dashLine(lines []line, keyLine, dash int) int {
	if keyLine > 0 && keyLine < len(lines) && !hasDash(lines[keyLine].text, dash) && hasDash(lines[keyLine-1].text, dash) {
		return keyLine - 1
	}
	return keyLine
}

// Text is the entry as written, its own comments included, de-indented into a
// mapping of its own: what `show` hands out and `apply` takes back. Line
// breaks come out as \n, except the NEL, LS and PS a quoted value may hold.
func (f *File) Text(it Item) string {
	if it.first < 0 {
		out, _ := yaml.Marshal(it.node)
		return string(out)
	}
	key := keyCol(it, f.dash)
	start := dashLine(f.lines, it.node.Line-1, f.dash)
	var b strings.Builder
	for l := it.first; l <= it.last && l < len(f.lines); l++ {
		t, brk := f.lines[l].text, f.lines[l].brk
		if l == start && hasDash(t, f.dash) {
			t = t[:f.dash] + " " + t[f.dash+1:]
			if blank(t) {
				continue // a dash on its own line
			}
		}
		b.WriteString(t[min(key, indent(t)):])
		if brk == "" || plainBreak(brk) {
			brk = "\n"
		}
		b.WriteString(brk)
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

// docMarker reports whether a line starts or ends a YAML document.
func docMarker(t string) bool {
	for _, m := range []string{"---", "..."} {
		if t == m || strings.HasPrefix(t, m+" ") || strings.HasPrefix(t, m+"\t") {
			return true
		}
	}
	return false
}

func hasDash(line string, col int) bool {
	if col < 0 || len(line) <= col || line[col] != '-' || indent(line) != col {
		return false
	}
	return len(line) == col+1 || line[col+1] == ' '
}

func indent(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

func blank(line string) bool { return strings.TrimSpace(line) == "" }

func isComment(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "#") }
