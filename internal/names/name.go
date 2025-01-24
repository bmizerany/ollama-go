package names

import (
	"fmt"
	"strings"
)

const MaxNameLength = 50 + 1 + 50 + 1 + 50 // <namespace>/<model>:<tag>

type Name struct {
	h string
	n string
	m string
	t string
}

func (n Name) Host() string      { return n.h }
func (n Name) Namespace() string { return n.n }
func (n Name) Model() string     { return n.m }
func (n Name) Tag() string       { return n.t }

func parseName(s string) Name {
	if len(s) > MaxNameLength {
		return Name{}
	}

	var n Name
	var tail string
	var c byte
	for {
		s, tail, c = cutLastAny(s, "/:")
		switch c {
		case ':':
			n.t = tail
			continue // look for model
		case '/':
			n.h, n.n, _ = cutLastAny(s, "/")
			n.m = tail
			return n
		case 0:
			n.m = tail
			return n
		}
	}
}

// String returns the fully qualified name in the format
// <namespace>/<model>:<tag>.
func (n Name) String() string {
	var b strings.Builder
	if n.n != "" {
		b.WriteString(n.n)
		b.WriteByte('/')
	}
	b.WriteString(n.m)
	if n.t != "" {
		b.WriteByte(':')
		b.WriteString(n.t)
	}
	return b.String()
}

func (n Name) GoString() string {
	return fmt.Sprintf("<Name %q %q %q %q>", n.h, n.n, n.m, n.t)
}

// IsFullyQualified returns true if the name is fully qualified, i.e. it has
// a host, Namespace, Model, and Tag.
func (n Name) IsValid() bool {
	return n.h != "" && n.n != "" && n.m != "" && n.t != ""
}

// cutLastAny is like strings.Cut but scans in reverse for the last character
// in chars. If no character is found, before is the empty string and after is
// s. The returned sep is the byte value of the character in chars if one was
// found; otherwise it is 0.
func cutLastAny(s, chars string) (before, after string, sep byte) {
	i := strings.LastIndexAny(s, chars)
	if i >= 0 {
		return s[:i], s[i+1:], s[i]
	}
	return "", s, 0
}
