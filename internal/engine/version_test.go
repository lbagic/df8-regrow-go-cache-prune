package engine

import "testing"

func TestCompareVersion(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"15.2", "15.2", 0},
		{"15", "15.0", 0},
		{"15.2", "15.10", -1}, // numeric, not lexical
		{"26", "15.5", 1},
		{"14.9", "15", -1},
		{"15.2.1", "15.2", 1},
	}
	for _, tt := range tests {
		if got := compareVersion(tt.a, tt.b); got != tt.want {
			t.Errorf("compareVersion(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := compareVersion(tt.b, tt.a); got != -tt.want {
			t.Errorf("compareVersion(%q, %q) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}
