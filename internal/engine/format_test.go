package engine

import "testing"

func TestSizeText(t *testing.T) {
	tests := []struct {
		bytes   int64
		partial bool
		size    string
		note    string
	}{
		{3 << 30, false, "3.0 GiB", ""},
		{0, true, "unreadable", "unreadable: " + UnreadableNote},
		{3 << 30, true, "≥ 3.0 GiB", "some folders unreadable: " + UnreadableNote},
	}
	for _, tt := range tests {
		if got := SizeText(tt.bytes, tt.partial); got != tt.size {
			t.Errorf("SizeText(%d, %v) = %q, want %q", tt.bytes, tt.partial, got, tt.size)
		}
		if got := PartialText(tt.bytes, tt.partial); got != tt.note {
			t.Errorf("PartialText(%d, %v) = %q, want %q", tt.bytes, tt.partial, got, tt.note)
		}
	}
}
