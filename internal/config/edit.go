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
// not mean to change it does not re-encode, so comments, blank lines, quoting
// and flow lists survive everywhere except in the lines it replaces. Only when
// a change cannot be made line by line (a flow-style entry, a key on the
// dash's own line that has to go) is the one entry re-encoded as a whole.

// ErrNotSpliceable means the automations are a flow list ([...]), which has no
// lines of its own to replace.
var ErrNotSpliceable = errors.New("automations is written as a flow list ([...]); rewrite it as a block list to edit it here")

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
	lines := splitLines(f.Raw)
	return joinLines(lines[:it.first], entryLines(text, f.dash, keyCol(it, f.dash)), lines[it.last+1:]), nil
}

// Append is the file with text added as its last entry, set off by a blank
// line when the entries before it are.
func (f *File) Append(text string) ([]byte, error) {
	lines := splitLines(f.Raw)
	if n := len(f.Items); n > 0 {
		last := f.Items[n-1]
		if last.first < 0 {
			return nil, ErrNotSpliceable
		}
		insert := entryLines(text, f.dash, keyCol(last, f.dash))
		if n > 1 && slices.ContainsFunc(lines[f.Items[n-2].last+1:last.first], blank) {
			insert = append([]string{""}, insert...)
		}
		return joinLines(lines[:last.last+1], insert, lines[last.last+1:]), nil
	}

	insert := entryLines(text, 2, 4)
	if f.keyLine >= 0 {
		// "automations:" with nothing, ~ or [] after it.
		head := lines[f.keyLine]
		i := strings.Index(head, "automations:")
		lines[f.keyLine] = head[:i+len("automations:")]
		return joinLines(lines[:f.keyLine+1], insert, lines[f.keyLine+1:]), nil
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return joinLines(lines, []string{"automations:"}, insert, []string{""}), nil
}

// Remove is the file without the entry: its lines, its heading, and the blank
// lines that set it off, so the gap where it was is the one its neighbours
// already had.
func (f *File) Remove(it Item) ([]byte, error) {
	if it.first < 0 {
		return nil, ErrNotSpliceable
	}
	lines := splitLines(f.Raw)
	i := slices.IndexFunc(f.Items, func(x Item) bool { return x.Line == it.Line })
	from, to := it.first, it.last+1
	switch {
	case i+1 < len(f.Items):
		for to < f.Items[i+1].first && blank(lines[to]) {
			to++
		}
	case i > 0:
		for from > f.Items[i-1].last+1 && blank(lines[from-1]) {
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
	lines := splitLines(f.Raw)
	dashLine := dashLine(lines, it.node.Line-1, f.dash)
	region, err := editMapping(lines[it.first:it.last+1], it.node, it.first, dashLine-it.first, set, del)
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
	return joinLines(lines[:it.first], region, lines[it.last+1:]), nil
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
	lines := splitLines([]byte(strings.TrimRight(text, "\n")))
	region, err := editMapping(lines, doc.Content[0], 0, -1, set, del)
	if errors.Is(err, errReEncode) {
		return reEncode(doc.Content[0], set, del)
	}
	if err != nil {
		return "", err
	}
	return strings.Join(region, "\n") + "\n", nil
}

// SetDisabled pauses or resumes an entry. Pausing sets disabled: true (in place
// or as a new last key); resuming removes the key. changed is false when the
// entry already was that way.
func SetDisabled(name string, disabled bool, ifMatch string) (changed bool, before, after *File, err error) {
	before, after, err = Change(ifMatch, false, func(f *File) (Edit, error) {
		it := f.Item(name)
		if it == nil {
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
		var err error
		if disabled {
			e.Raw, err = f.SetKeys(*it, []Pair{{Key: "disabled", Value: &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}}}, nil)
		} else {
			e.Raw, err = f.SetKeys(*it, nil, []string{"disabled"})
		}
		return e, err
	})
	return changed, before, after, err
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
// column. It is the inverse of Text.
func entryLines(text string, dash, key int) []string {
	src := strings.Split(strings.TrimRight(strings.TrimLeft(text, "\n"), "\n"), "\n")
	out := make([]string, 0, len(src))
	seenKey := false
	for _, l := range src {
		switch {
		case !seenKey && isComment(l):
			out = append(out, strings.Repeat(" ", dash)+strings.TrimLeft(l, " "))
		case !seenKey && blank(l):
			out = append(out, "")
		case !seenKey:
			seenKey = true
			out = append(out, strings.Repeat(" ", dash)+"-"+strings.Repeat(" ", key-dash-1)+l)
		case l == "":
			out = append(out, "")
		default:
			out = append(out, strings.Repeat(" ", key)+l)
		}
	}
	return out
}

func joinLines(parts ...[]string) []byte {
	var all []string
	for _, p := range parts {
		all = append(all, p...)
	}
	return []byte(strings.Join(all, "\n"))
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
// entry's dash, or -1. It returns the new lines, or errReEncode.
func editMapping(lines []string, m *yaml.Node, base, dashAt int, set []Pair, del []string) ([]string, error) {
	if m.Kind != yaml.MappingNode || m.Style&yaml.FlowStyle != 0 {
		return nil, errReEncode
	}
	col := m.Content[0].Column - 1
	spans := make([]keySpan, len(m.Content)/2)
	for i := range spans {
		k, v := m.Content[2*i], m.Content[2*i+1]
		spans[i] = keySpan{key: k.Line - 1 - base, k: k, v: v}
	}
	for i := range spans {
		lower := 0
		if i > 0 {
			lower = spans[i-1].key + 1
		}
		h := spans[i].key
		for h-1 >= lower && isComment(lines[h-1]) && indent(lines[h-1]) == col {
			h--
		}
		spans[i].head = h
	}
	for i := range spans {
		bound := len(lines)
		if i+1 < len(spans) {
			bound = spans[i+1].head
		}
		spans[i].end = spans[i].key
		for l := spans[i].key + 1; l < bound; l++ {
			if !blank(lines[l]) {
				spans[i].end = l
			}
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
		repl     []string
	}
	var changes []change
	var appended []string
	for _, p := range set {
		s := find(p.Key)
		if s == nil {
			appended = append(appended, pairLines(p.Key, styled(p.Value, nil), col)...)
			continue
		}
		if sameValue(s.v, p.Value) {
			continue
		}
		v := styled(p.Value, s.v)
		if line, ok := inPlace(lines[s.key], s, v); ok {
			changes = append(changes, change{s.key, s.key + 1, []string{line}})
			continue
		}
		if s.key == dashAt {
			return nil, errReEncode
		}
		changes = append(changes, change{s.key, s.end + 1, pairLines(p.Key, v, col)})
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
		at := spans[len(spans)-1].end + 1
		changes = append(changes, change{at, at, appended})
	}

	sort.SliceStable(changes, func(i, j int) bool { return changes[i].from > changes[j].from })
	out := slices.Clone(lines)
	for _, c := range changes {
		out = slices.Concat(out[:c.from], c.repl, out[c.to:])
	}
	return out, nil
}

// inPlace rewrites just the value of a one-line scalar on its own line,
// keeping whatever follows it (a comment, its spacing).
func inPlace(line string, s *keySpan, v *yaml.Node) (string, bool) {
	if s.v.Kind != yaml.ScalarNode || v.Kind != yaml.ScalarNode || s.v.Line != s.k.Line || s.end != s.key ||
		s.v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return "", false
	}
	value, err := encode(v)
	if err != nil || strings.Contains(value, "\n") {
		return "", false
	}
	start := s.v.Column - 1
	if start > len(line) {
		return "", false
	}
	return line[:start] + value + line[tokenEnd(line, start, s.v):], true
}

// tokenEnd finds where a scalar written at start ends on its line.
func tokenEnd(line string, start int, v *yaml.Node) int {
	switch {
	case v.Style&yaml.DoubleQuotedStyle != 0:
		for i := start + 1; i < len(line); i++ {
			if line[i] == '\\' {
				i++
			} else if line[i] == '"' {
				return i + 1
			}
		}
	case v.Style&yaml.SingleQuotedStyle != 0:
		for i := start + 1; i < len(line); i++ {
			if line[i] == '\'' {
				if i+1 < len(line) && line[i+1] == '\'' {
					i++
					continue
				}
				return i + 1
			}
		}
	default:
		// A plain scalar ends where its comment's run of spaces begins.
		if j := strings.Index(line[start:], " #"); j >= 0 {
			return start + len(strings.TrimRight(line[start:start+j], " \t"))
		}
	}
	return len(line)
}

// pairLines renders one key and its value at a column.
func pairLines(key string, v *yaml.Node, col int) []string {
	text, err := encode(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v,
	}})
	if err != nil {
		return nil
	}
	out := strings.Split(text, "\n")
	for i, l := range out {
		if l != "" {
			out[i] = strings.Repeat(" ", col) + l
		}
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
// writes by hand. Multi-line strings become literal blocks.
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
	return c
}

// plain is a deep copy of v without any styles or comments of its own.
func plain(v *yaml.Node) *yaml.Node {
	c := &yaml.Node{Kind: v.Kind, Tag: v.Tag, Value: v.Value}
	for _, child := range v.Content {
		cc := plain(child)
		if cc.Kind == yaml.ScalarNode && strings.Contains(cc.Value, "\n") {
			cc.Style = yaml.LiteralStyle
		}
		c.Content = append(c.Content, cc)
	}
	return c
}

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
