package trash

import "testing"

func TestGuardPath(t *testing.T) {
	home := "/Users/t"
	tests := []struct {
		path string
		ok   bool
	}{
		{"", false},
		{"   ", false},
		{"relative/path", false},
		{"/", false},
		{"/..", false},
		{"/Users/t", false},        // home
		{"/Users/t/../t", false},   // home, sneaky
		{"/Users", false},          // top-level
		{"/Applications", false},   // top-level
		{"/Volumes", false},        // top-level
		{"/Volumes/Backup", false}, // mount root
		{"/Volumes/Backup/.Trashes/501", true},
		{"/Users/t/Library/Caches/go-build", true},
		{"/Applications/Install macOS Sonoma.app", true},
		{"/Library/Application Support/com.apple.idleassetsd/Customer", true},
	}
	for _, tt := range tests {
		err := GuardPath(tt.path, home)
		if tt.ok && err != nil {
			t.Errorf("GuardPath(%q) rejected: %v", tt.path, err)
		}
		if !tt.ok && err == nil {
			t.Errorf("GuardPath(%q) allowed", tt.path)
		}
	}
}
