package names

import (
	"strings"
)

const MaxNameLength = 50 + 1 + 50 + 1 + 50 // <namespace>/<model>:<tag>

type Name struct {
	host      string
	namespace string
	model     string
	tag       string
}

func (n Name) Host() string      { return n.host }
func (n Name) Namespace() string { return n.namespace }
func (n Name) Model() string     { return n.model }
func (n Name) Tag() string       { return n.tag }

// h/n/m:t
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
			n.tag = tail
		case '/':
			n.namespace = s
			n.model = tail
			n.host, _, _ = cutLastAny(s, "/")
			return n
		case 0:
			n.model = s
			return n
		}
	}
}

// String returns the fully qualified name in the format
// <namespace>/<model>:<tag>.
func (n Name) String() string {
	var b strings.Builder
	if n.namespace != "" {
		b.WriteString(n.namespace)
		b.WriteByte('/')
	}
	b.WriteString(n.model)
	if n.tag != "" {
		b.WriteByte(':')
		b.WriteString(n.tag)
	}
	return b.String()
}

// IsFullyQualified returns true if the name is fully qualified, i.e. it has
// a host, Namespace, Model, and Tag.
func (n Name) IsValid() bool {
	return n.host != "" && n.namespace != "" && n.model != "" && n.tag != ""
}

func cutLastAny(s, seps string) (before, after string, sep byte) {
	i := strings.LastIndexAny(s, seps)
	if i < 0 {
		return s, "", 0
	}
	return s[:i], s[i+1:], s[i]
}
