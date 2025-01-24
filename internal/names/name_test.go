package names

import (
	"strings"
	"testing"
)

func TestParseName(t *testing.T) {
	cases := []struct {
		in   string
		want Name
	}{
		{"", Name{}},
		{"m:t", Name{m: "m", t: "t"}},
		{"m", Name{m: "m"}},
		{"/m", Name{m: "m"}},
		{"/n/m:t", Name{n: "n", m: "m", t: "t"}},
		{"n/m", Name{n: "n", m: "m"}},
		{"n/m:t", Name{n: "n", m: "m", t: "t"}},
		{"n/m", Name{n: "n", m: "m"}},
		{"n/m", Name{n: "n", m: "m"}},
		{strings.Repeat("m", MaxNameLength+1), Name{}},
		{"h/n/m:t", Name{h: "h", n: "n", m: "m", t: "t"}},

		// Invalids
		// TODO: {"n:t/m:t", Name{}},
		// TODO: {"/h/n/m:t", Name{}},
	}

	for _, tt := range cases {
		t.Run(tt.in, func(t *testing.T) {
			got := Parse(tt.in)
			if got.Compare(tt.want) != 0 {
				t.Errorf("parseName(%q) = %#v, want %q", tt.in, got, tt.want)
			}
		})
	}
}

var junkName Name

func BenchmarkParseName(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		junkName = Parse("h/n/m:t")
	}
}
