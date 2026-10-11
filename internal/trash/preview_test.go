package trash

import (
	"strings"
	"testing"
)

func TestPreviewCommand(t *testing.T) {
	argv := PreviewCommand("/Users/dev/Library/Caches/go-build")
	if len(argv) != 3 || argv[0] != "osascript" || argv[1] != "-e" {
		t.Fatalf("unexpected argv: %v", argv)
	}
	if !strings.Contains(argv[2], `"/Users/dev/Library/Caches/go-build"`) {
		t.Errorf("script must quote the target path: %s", argv[2])
	}
	if !strings.Contains(argv[2], `"Finder"`) {
		t.Errorf("script must address Finder so Put Back works: %s", argv[2])
	}
}

// Paths with quotes must not break out of the AppleScript string.
func TestPreviewCommandEscapesQuotes(t *testing.T) {
	argv := PreviewCommand(`/Users/dev/we"ird`)
	if !strings.Contains(argv[2], `"/Users/dev/we\"ird"`) {
		t.Errorf("quote not escaped: %s", argv[2])
	}
}
