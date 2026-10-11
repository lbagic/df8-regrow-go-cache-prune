package jsonl

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFrame(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		want     string
	}{
		{"empty file", "", "{}\n"},
		{"after a whole line", "{\"a\":1}\n", "{}\n"},
		{"after a cut-short line", "{\"a\":", "\n{}\n"},
	}
	for _, tt := range tests {
		path := filepath.Join(t.TempDir(), "j.jsonl")
		if err := os.WriteFile(path, []byte(tt.existing), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(Frame(f, []byte("{}"))); got != tt.want {
			t.Errorf("%s: Frame = %q, want %q", tt.name, got, tt.want)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFrameAssumesAFragmentWhenTheFileCannotBeRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	if err := os.WriteFile(path, []byte("{\"a\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Write-only: the last byte cannot be read back.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if got := string(Frame(f, []byte("{}"))); got != "\n{}\n" {
		t.Errorf("Frame = %q, want a leading newline", got)
	}
}
