package config

import (
	"bytes"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// The editing half of spans.go. Every edit here works on lines: what it does
// not mean to change it does not re-encode, so comments, blank lines, quoting,
// flow lists and line endings survive everywhere except in the lines it
// replaces. Only when a change cannot be made line by line (a flow-style
// entry, a key on the dash's own line that has to go) is the one entry
// re-encoded as a whole. Change checks the result either way.

// ErrNotSpliceable means the entry, or the list it is in, cannot be edited
// line by line: a flow list ([...]), an anchor from elsewhere, or a layout
// whose lines do not read back as the entry they hold.
var ErrNotSpliceable = errors.New("this entry cannot be edited here: the list is a flow list ([...]), " +
	"or the entry uses an anchor from elsewhere or an unusual layout; edit automations.yaml by hand")

// Pair is one key of an entry and the value it should get.
type Pair struct {
	Key   string
	Value *yaml.Node
}

// Item returns the first entry by that name, loaded or not, or nil.
func (f *File) Item(name string) *Item {
	for i := range f.Items {
		if f.Items[i].Name == name {
			return &f.Items[i]
		}
	}
	return nil
}

// Replace is the file with the entry's lines replaced by text: a mapping at
// column 0, as Text returns it.
func (f *File) Replace(it Item, text string) ([]byte, error) {
	if it.first < 0 {
		return nil, ErrNotSpliceable
	}
	repl := entryLines(text, f.dash, keyCol(it, f.dash), newline(f.lines))
	return splice(f.lines, it.first, it.last+1, repl), nil
}

// Append is the file with text added as its last entry, set off by a blank
// line when the entries before it are.
func (f *File) Append(text string) ([]byte, error) {
	lines, nl := f.lines, newline(f.lines)
	if n := len(f.Items); n > 0 {
		last := f.Items[n-1]
		if last.first < 0 {
			return nil, ErrNotSpliceable
		}
		insert := entryLines(text, f.dash, keyCol(last, f.dash), nl)
		if n > 1 {
			if prev := f.Items[n-2]; prev.first >= 0 &&
				slices.ContainsFunc(lines[prev.last+1:last.first], func(l line) bool { return blank(l.text) }) {
				insert = append([]line{{"", nl}}, insert...)
			}
		}
		return joinLines(ended(lines[:last.last+1], nl), insert, lines[last.last+1:]), nil
	}

	insert := entryLines(text, 2, 4, nl)
	if f.keyLine >= 0 {
		// "automations:" with nothing, ~ or [] after it.
		head := lines[f.keyLine]
		i := strings.Index(head.text, "automations:")
		if i < 0 {
			return nil, ErrNotSpliceable
		}
		before := ended(lines[:f.keyLine+1], nl)
		before[f.keyLine].text = head.text[:i+len("automations:")]
		return joinLines(before, insert, lines[f.keyLine+1:]), nil
	}
	for len(lines) > 0 && blank(lines[len(lines)-1].text) {
		lines = lines[:len(lines)-1]
	}
	return joinLines(ended(lines, nl), []line{{"automations:", nl}}, insert), nil
}

// Remove is the file without the entry: its lines, its heading, and the blank
// lines that set it off, so the gap where it was is the one its neighbours
// already had.
func (f *File) Remove(it Item) ([]byte, error) {
	if it.first < 0 {
		return nil, ErrNotSpliceable
	}
	lines := f.lines
	i := slices.IndexFunc(f.Items, func(x Item) bool { return x.Line == it.Line })
	from, to := it.first, it.last+1
	switch {
	case i+1 < len(f.Items) && f.Items[i+1].first >= 0:
		for to < f.Items[i+1].first && blank(lines[to].text) {
			to++
		}
	case i > 0 && f.Items[i-1].first >= 0:
		for from > f.Items[i-1].last+1 && blank(lines[from-1].text) {
			from--
		}
	}
	return joinLines(lines[:from], lines[to:]), nil
}

// SetKeys is the file with keys of one entry changed: each Pair's key gets its
// value (in place if the key is there, appended if not), each key in del goes.
// A value equal to the one there is left alone, formatting and all.
func (f *File) SetKeys(it Item, set []Pair, del []string) ([]byte, error) {
	if it.first < 0 {
		return nil, ErrNotSpliceable
	}
	if it.node.Kind != yaml.MappingNode {
		return nil, errors.New("this entry is not a mapping of keys; fix it in automations.yaml")
	}
	if !plainKeys(f.lines, it.node) {
		return nil, errors.New("this entry's keys are not plain names (an explicit ? key, a number, a tag); fix it in automations.yaml")
	}
	dl := dashLine(f.lines, it.node.Line-1, f.dash)
	region, err := editMapping(slices.Clone(f.lines[it.first:it.last+1]), it.node, it.first, dl-it.first,
		set, del, newline(f.lines))
	if errors.Is(err, errReEncode) {
		text, err := reEncode(it.node, set, del)
		if err != nil {
			return nil, err
		}
		return f.Replace(it, text)
	}
	if err != nil {
		return nil, err
	}
	return splice(f.lines, it.first, it.last+1, region), nil
}

// NewEntryText writes a new entry from its keys, the way someone would write
// it by hand rather than the way JSON spelled it.
func NewEntryText(set []Pair) (string, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, p := range set {
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: p.Key}, styled(p.Value, nil))
	}
	text, err := encode(m)
	return text + "\n", err
}

// SetKeysInText does what SetKeys does, to a mapping given as text.
func SetKeysInText(text string, set []Pair, del []string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return "", err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", errors.New("not a mapping")
	}
	lines := splitLines([]byte(text))
	if n := len(lines); n > 1 && lines[n-1] == (line{}) {
		lines = lines[:n-1] // what the final line break leaves behind
	}
	region, err := editMapping(lines, doc.Content[0], 0, -1, set, del, newline(lines))
	if errors.Is(err, errReEncode) {
		return reEncode(doc.Content[0], set, del)
	}
	if err != nil {
		return "", err
	}
	return string(joinLines(ended(region, "\n"))), nil
}

// SetDisabled pauses or resumes an entry. Pausing sets disabled: true (in place
// or as a new last key); resuming removes the key. changed is false when the
// entry already was that way.
func SetDisabled(name string, disabled bool, ifMatch string) (changed bool, before, after *File, err error) {
	before, after, err = Change(ifMatch, false, func(f *File) (Edit, error) {
		it := f.Item(name)
		if it == nil || name == "" {
			return Edit{}, &NotFoundError{Name: name}
		}
		a := f.Find(name)
		if a == nil || a.Line != it.Line {
			refusal := &RefusedError{Message: name + " did not load; fix it first"}
			if d := f.Diagnostic(name); d != nil {
				refusal.Message, refusal.Diagnostics = d.Message, []Diagnostic{*d}
			}
			return Edit{}, refusal
		}
		e := Edit{Raw: f.Raw, Touched: []string{name}, MustLoad: []string{name}}
		if a.Disabled == disabled {
			return e, nil
		}
		changed = true
		want, err := it.Map()
		if err != nil {
			return Edit{}, err
		}
		if disabled {
			want["disabled"] = true
			e.Raw, err = f.SetKeys(*it, []Pair{{Key: "disabled", Value: &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}}}, nil)
		} else {
			delete(want, "disabled")
			e.Raw, err = f.SetKeys(*it, nil, []string{"disabled"})
		}
		e.Entry, e.Want = name, want
		return e, err
	})
	return changed, before, after, err
}

// Map is the entry as written, as plain data. An entry that is not a mapping
// of keys has none.
func (it Item) Map() (map[string]any, error) {
	if it.node.Kind != yaml.MappingNode {
		return nil, errors.New("not a mapping of keys")
	}
	m := map[string]any{}
	err := it.node.Decode(&m)
	return m, err
}

// keyCol is the column an entry's keys sit at.
func keyCol(it Item, dash int) int {
	if c := it.node.Column - 1; c > dash {
		return c
	}
	return dash + 2
}

// entryLines lays an entry's text (a mapping at column 0) into the list: a
// heading at the dash, the first key on the dash's line, the rest at the key
// column, every plain line break the file's own. It is the inverse of Text.
// Trailing blank lines go, unless the last value is kept with |+, whose value
// they are.
func entryLines(text string, dash, key int, nl string) []line {
	src := splitLines([]byte(text))
	for len(src) > 0 && blank(src[0].text) {
		src = src[1:]
	}
	if n := len(src); n > 0 && src[n-1] == (line{}) {
		src = src[:n-1]
	}
	var doc yaml.Node
	keep := yaml.Unmarshal([]byte(text), &doc) == nil && len(doc.Content) == 1 &&
		keepsTrailing(splitLines([]byte(text)), lastValue(doc.Content[0]))
	for !keep && len(src) > 0 && blank(src[len(src)-1].text) {
		src = src[:len(src)-1]
	}
	out := make([]line, 0, len(src))
	seenKey := false
	for _, l := range src {
		t, brk := l.text, l.brk
		if brk == "" || plainBreak(brk) {
			brk = nl
		}
		switch {
		case !seenKey && isComment(t):
			t = strings.Repeat(" ", dash) + strings.TrimLeft(t, " ")
		case !seenKey && blank(t):
			t = ""
		case !seenKey:
			seenKey = true
			t = strings.Repeat(" ", dash) + "-" + strings.Repeat(" ", max(1, key-dash-1)) + t
		case t == "":
		default:
			t = strings.Repeat(" ", key) + t
		}
		out = append(out, line{t, brk})
	}
	return out
}

// splice is lines with lines[from:to] replaced by repl. A file that ended
// without a line break still does.
func splice(lines []line, from, to int, repl []line) []byte {
	if to == len(lines) && len(repl) > 0 && lines[len(lines)-1].brk == "" {
		repl = slices.Clone(repl)
		repl[len(repl)-1].brk = ""
	}
	return joinLines(lines[:from], repl, lines[to:])
}

// ended is a copy of lines whose last one ends in a break, so more can follow.
func ended(lines []line, nl string) []line {
	out := slices.Clone(lines)
	if n := len(out); n > 0 && out[n-1].brk == "" {
		out[n-1].brk = nl
	}
	return out
}

// errReEncode means a change cannot be made line by line.
var errReEncode = errors.New("re-encode")

// keySpan is where one key of a mapping sits: head is its first comment line
// above it, end its last line. Indices are into the lines handed to
// editMapping.
type keySpan struct {
	head, key, end int
	k, v           *yaml.Node
}

// editMapping changes keys of the block mapping m, whose lines are lines
// (m's positions count from base). dashAt is the index of the line holding the
// entry's dash, or -1. New lines get nl. It returns the new lines, or
// errReEncode for anything it cannot do line by line.
func editMapping(lines []line, m *yaml.Node, base, dashAt int, set []Pair, del []string, nl string) ([]line, error) {
	if m.Kind != yaml.MappingNode || m.Style&yaml.FlowStyle != 0 || len(m.Content) < 2 {
		return nil, errReEncode
	}
	// A last line without a break (the end of the file) gets one, or a key
	// appended after it would land on it. splice takes it off again.
	lines = ended(lines, nl)
	col := m.Content[0].Column - 1
	spans := make([]keySpan, len(m.Content)/2)
	for i := range spans {
		k, v := m.Content[2*i], m.Content[2*i+1]
		key := k.Line - 1 - base
		if key < 0 || key >= len(lines) || (i > 0 && key <= spans[i-1].key) {
			return nil, errReEncode // the nodes and the lines disagree: do not guess
		}
		spans[i] = keySpan{key: key, k: k, v: v}
	}
	for i := range spans {
		lower := 0
		if i > 0 {
			lower = spans[i-1].key + 1
		}
		h := spans[i].key
		for h-1 >= lower && isComment(lines[h-1].text) && indent(lines[h-1].text) == col {
			h--
		}
		spans[i].head = h
	}
	for i := range spans {
		bound := len(lines)
		if i+1 < len(spans) {
			bound = spans[i+1].head
		}
		s := &spans[i]
		s.end = s.key
		for l := s.key + 1; l < bound; l++ {
			if !blank(lines[l].text) {
				s.end = l
			}
		}
		if keptAt(lines, s.v, base) {
			s.end = bound - 1 // a |+ value owns the blank lines after it
		}
	}
	find := func(key string) *keySpan {
		for i := range spans {
			if spans[i].k.Value == key {
				return &spans[i]
			}
		}
		return nil
	}

	type change struct {
		from, to int // [from, to) replaced by repl
		repl     []line
	}
	var changes []change
	var appended []line
	for _, p := range set {
		s := find(p.Key)
		if s == nil {
			appended = append(appended, pairLines(p.Key, styled(p.Value, nil), col, nl)...)
			continue
		}
		if sameValue(s.v, p.Value) {
			continue
		}
		v := styled(p.Value, s.v)
		if l, ok := inPlace(lines[s.key], s, v); ok {
			changes = append(changes, change{s.key, s.key + 1, []line{l}})
			continue
		}
		if s.key == dashAt {
			return nil, errReEncode
		}
		changes = append(changes, change{s.key, s.end + 1, pairLines(p.Key, v, col, nl)})
	}
	for _, key := range del {
		s := find(key)
		if s == nil {
			continue
		}
		if s.key == dashAt {
			return nil, errReEncode
		}
		changes = append(changes, change{s.head, s.end + 1, nil})
	}
	if len(appended) > 0 {
		last := spans[len(spans)-1]
		at := last.end + 1
		if keptAt(lines, last.v, base) {
			// After a |+ value, a new key would cut its trailing blank lines
			// off: the new keys go above the last key instead.
			at = last.head
		}
		changes = append(changes, change{at, at, appended})
	}

	sort.SliceStable(changes, func(i, j int) bool { return changes[i].from > changes[j].from })
	out := lines
	for i, c := range changes {
		if i > 0 && c.to > changes[i-1].from {
			return nil, errReEncode // two changes reach into each other
		}
		out = slices.Concat(out[:c.from], c.repl, out[c.to:])
	}
	return out, nil
}

// plainKeys reports whether every key of the mapping is a plain string, and,
// in block style, starts its own line (after the dash on the first): the
// shape editMapping and reEncode both assume.
func plainKeys(lines []line, m *yaml.Node) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
			return false
		}
		if m.Style&yaml.FlowStyle != 0 {
			continue
		}
		if k.Line < 1 || k.Line > len(lines) {
			return false
		}
		t := lines[k.Line-1].text
		before := strings.TrimLeft(t[:byteAt(t, k.Column-1)], " ")
		if before != "" && strings.TrimRight(before, " ") != "-" {
			return false
		}
	}
	return true
}

// keptAt is keepsTrailing for lines that start at line base of the file.
func keptAt(lines []line, v *yaml.Node, base int) bool {
	i := v.Line - 1 - base
	return i >= 0 && i < len(lines) && keptBlock(lines[i].text, v)
}

// inPlace rewrites just the value of a one-line scalar on its own line,
// keeping whatever follows it (a comment, its spacing) and its line break.
func inPlace(l line, s *keySpan, v *yaml.Node) (line, bool) {
	if s.v.Kind != yaml.ScalarNode || v.Kind != yaml.ScalarNode || s.v.Line != s.k.Line || s.end != s.key ||
		s.v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return line{}, false
	}
	value, err := encode(v)
	if err != nil || strings.ContainsAny(value, "\n\r") {
		return line{}, false
	}
	t := l.text
	start := byteAt(t, s.v.Column-1)
	prefix := t[:start]
	if strings.HasSuffix(prefix, ":") {
		prefix += " " // an empty value sits right after the colon
	}
	return line{prefix + value + t[tokenEnd(t, start, s.v):], l.brk}, true
}

// tokenEnd finds where a scalar written at start ends on its line.
func tokenEnd(t string, start int, v *yaml.Node) int {
	switch {
	case v.Style&yaml.DoubleQuotedStyle != 0:
		for i := start + 1; i < len(t); i++ {
			if t[i] == '\\' {
				i++
			} else if t[i] == '"' {
				return i + 1
			}
		}
	case v.Style&yaml.SingleQuotedStyle != 0:
		for i := start + 1; i < len(t); i++ {
			if t[i] == '\'' {
				if i+1 < len(t) && t[i+1] == '\'' {
					i++
					continue
				}
				return i + 1
			}
		}
	default:
		// A plain scalar ends where the blanks before its comment begin; a
		// comment needs a space or a tab in front of its #.
		for i := start; i < len(t); i++ {
			if t[i] == '#' && i > 0 && (t[i-1] == ' ' || t[i-1] == '\t') {
				return start + len(strings.TrimRight(t[start:i], " \t"))
			}
		}
	}
	return len(t)
}

// pairLines renders one key and its value at a column.
func pairLines(key string, v *yaml.Node, col int, nl string) []line {
	text, err := encode(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v,
	}})
	if err != nil {
		return nil
	}
	out := splitLines([]byte(text))
	for i := range out {
		if out[i].text != "" {
			out[i].text = strings.Repeat(" ", col) + out[i].text
		}
		out[i].brk = nl
	}
	return out
}

// reEncode applies the change to a copy of the mapping and encodes all of it:
// for what editMapping cannot do line by line. Comments attached to the keys
// survive; blank lines inside the entry do not.
func reEncode(m *yaml.Node, set []Pair, del []string) (string, error) {
	c := *m
	c.Style = 0
	c.Content = slices.Clone(m.Content)
	for _, p := range set {
		if i := keyIndex(&c, p.Key); i >= 0 {
			c.Content[i+1] = styled(p.Value, c.Content[i+1])
		} else {
			c.Content = append(c.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: p.Key}, styled(p.Value, nil))
		}
	}
	for _, key := range del {
		if i := keyIndex(&c, key); i >= 0 {
			c.Content = slices.Delete(c.Content, i, i+2)
		}
	}
	text, err := encode(&c)
	return text + "\n", err
}

func keyIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// styled is v as it should be written where old was: the old value's style
// when there was one of the same kind, idiomatic block YAML otherwise. Values
// that came from JSON arrive double-quoted and in flow style, which nobody
// writes by hand. Multi-line strings become literal blocks. A string holding a
// \r, NEL, LS or PS is double-quoted, the one style that escapes them: written
// raw, each is a line break to yaml.v3 and every line after it shifts.
func styled(v, old *yaml.Node) *yaml.Node {
	c := plain(v)
	switch {
	case old != nil && old.Kind == c.Kind && (c.Kind != yaml.ScalarNode || old.Tag == c.Tag):
		c.Style = old.Style
		if c.Kind == yaml.ScalarNode && strings.Contains(c.Value, "\n") && c.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0 {
			c.Style = yaml.LiteralStyle
		}
	case c.Kind == yaml.ScalarNode && strings.Contains(c.Value, "\n"):
		c.Style = yaml.LiteralStyle
	}
	if c.Kind == yaml.ScalarNode && hiddenBreak(c.Value) {
		c.Style = yaml.DoubleQuotedStyle
	}
	return c
}

// plain is a deep copy of v without any styles or comments of its own.
func plain(v *yaml.Node) *yaml.Node {
	c := &yaml.Node{Kind: v.Kind, Tag: v.Tag, Value: v.Value}
	for _, child := range v.Content {
		cc := plain(child)
		switch {
		case cc.Kind == yaml.ScalarNode && hiddenBreak(cc.Value):
			cc.Style = yaml.DoubleQuotedStyle
		case cc.Kind == yaml.ScalarNode && strings.Contains(cc.Value, "\n"):
			cc.Style = yaml.LiteralStyle
		}
		c.Content = append(c.Content, cc)
	}
	return c
}

// hiddenBreak reports whether s holds a character yaml.v3 breaks a line at,
// other than \n.
func hiddenBreak(s string) bool { return strings.ContainsAny(s, "\r\u0085  ") }

// sameValue compares two values as data, so the same list written in flow
// style or JSON counts as unchanged.
func sameValue(a, b *yaml.Node) bool {
	var x, y any
	if a.Decode(&x) != nil || b.Decode(&y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// encode writes a node the way the files here are written: two-space indent.
func encode(n *yaml.Node) (string, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}
