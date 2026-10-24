package config

import "unicode/utf8"

// The file is cut into lines the way yaml.v3 counts them, because a node's
// Line is what says where an entry is. yaml.v3 breaks a line at \r\n, at a
// lone \r or \n, and at NEL, LS and PS (U+0085, U+2028, U+2029). Splitting on
// \n alone put every entry after an LS in a quoted prompt a line off; edits
// then landed on the wrong line of the wrong entry, and slicing past a short
// line panicked the loader.

// line is one line and the break that ends it, which is "" for the last.
type line struct{ text, brk string }

// splitLines cuts raw where yaml.v3 counts a new line.
func splitLines(raw []byte) []line {
	var out []line
	start := 0
	for i := 0; i < len(raw); {
		n := breakAt(raw, i)
		if n == 0 {
			i++
			continue
		}
		out = append(out, line{string(raw[start:i]), string(raw[i : i+n])})
		i += n
		start = i
	}
	return append(out, line{string(raw[start:]), ""})
}

// breakAt is the length of the line break starting at raw[i], or 0.
func breakAt(raw []byte, i int) int {
	switch {
	case raw[i] == '\r' && i+1 < len(raw) && raw[i+1] == '\n':
		return 2
	case raw[i] == '\r', raw[i] == '\n':
		return 1
	case raw[i] == 0xC2 && i+1 < len(raw) && raw[i+1] == 0x85: // NEL
		return 2
	case raw[i] == 0xE2 && i+2 < len(raw) && raw[i+1] == 0x80 && (raw[i+2] == 0xA8 || raw[i+2] == 0xA9): // LS, PS
		return 3
	}
	return 0
}

// plainBreak reports whether a break only ends a line. NEL, LS and PS may sit
// inside a quoted value, where rewriting them would change the value; these
// three only ever end the line.
func plainBreak(brk string) bool { return brk == "\n" || brk == "\r\n" || brk == "\r" }

// joinLines puts lines back together, breaks and all.
func joinLines(parts ...[]line) []byte {
	var n int
	for _, p := range parts {
		for _, l := range p {
			n += len(l.text) + len(l.brk)
		}
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		for _, l := range p {
			out = append(out, l.text...)
			out = append(out, l.brk...)
		}
	}
	return out
}

// newline is the break new lines get: the file's own, so a CRLF file stays
// CRLF.
func newline(ls []line) string {
	for _, l := range ls {
		if plainBreak(l.brk) {
			return l.brk
		}
	}
	return "\n"
}

// byteAt turns a yaml.v3 column (0-based, in characters) into a byte offset in
// s, never past its end.
func byteAt(s string, col int) int {
	i := 0
	for n := 0; n < col && i < len(s); n++ {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return i
}
