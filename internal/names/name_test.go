package names

import (
	"strings"
	"testing"
)

func TestParseName(t *testing.T) {
	cases := []struct {
		in        string
		want      Name
		wantValid bool
	}{
		{"", Name{"", "", "", ""}, false},
		{"m:t", Name{"", "", "m", "t"}, false},
		{"m", Name{"", "", "m", ""}, false},
		{"/m", Name{"", "", "m", ""}, false},
		{"/n/m:t", Name{"", "n", "m", "t"}, false},
		{"n/m", Name{"", "n", "m", ""}, false},
		{"n/m:t", Name{"", "n", "m", "t"}, false},
		{"n/m", Name{"", "n", "m", ""}, false},
		{"n/m", Name{"", "n", "m", ""}, false},
		{"n:t/m:t", Name{"", "n:t", "m", "t"}, false},
		{strings.Repeat("m", MaxNameLength+1), Name{}, false},
	}

	for _, tt := range cases {
		t.Run(tt.in, func(t *testing.T) {
			got := parseName(tt.in)
			if got != tt.want {
				t.Errorf("parseName(%q) = %#v, want %q", tt.in, got, tt.want)
			}
			if tt.wantValid != parseName(tt.in).IsValid() {
				t.Errorf("IsValid(%q) = %v, want %v", tt.in, !tt.wantValid, tt.wantValid)
			}
		})
	}
}

var junkName Name

func BenchmarkParseName(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		junkName = parseName("h/n/m:t")
	}
}
