package names

import (
	"fmt"
	"strings"
)

const MaxNameLength = 50 + 1 + 50 + 1 + 50 // <namespace>/<model>:<tag>

type Name struct {
	host string
	n    string
	m    string
	t    string
}

func (n Name) Host() string      { return n.host }
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
		case '/':
			n.n = s
			n.m = tail
			return n
		case 0:
			n.m = s
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
	return fmt.Sprintf("<Name %q %q %q %q>", n.host, n.n, n.m, n.t)
}

// IsFullyQualified returns true if the name is fully qualified, i.e. it has
// a host, Namespace, Model, and Tag.
func (n Name) IsValid() bool {
	return n.host != "" && n.n != "" && n.m != "" && n.t != ""
}

func cutLastAny(s, chars string) (before, after string, sep byte) {
	i := strings.LastIndexAny(s, chars)
	if i >= 0 {
		return s[:i], s[i+1:], s[i]
	}
	return s, "", 0
}
