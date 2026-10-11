package rules

import (
	"io/fs"
	"testing"
)

func TestCatalogEmbedded(t *testing.T) {
	matches, err := fs.Glob(FS, "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no rules embedded")
	}
}
