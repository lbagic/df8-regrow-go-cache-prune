package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFileMissingIsDefaults(t *testing.T) {
	c, err := LoadFile(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	capBytes, err := c.Docker.ExportCapBytes()
	if err != nil {
		t.Fatal(err)
	}
	if capBytes != DefaultVolumeExportCap {
		t.Fatalf("default cap = %d, want %d", capBytes, DefaultVolumeExportCap)
	}
	if c.Docker.Keeps("anything", "any") {
		t.Fatal("empty keep-list must keep nothing")
	}
}

func TestLoadFileKeepAndCap(t *testing.T) {
	p := writeConfig(t, `
docker:
  keep:
    - dune_*
    - name: "prod-db"
    - project: osprey
  volume_export_cap: 2GiB
`)
	c, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	capBytes, err := c.Docker.ExportCapBytes()
	if err != nil {
		t.Fatal(err)
	}
	if capBytes != 2<<30 {
		t.Fatalf("cap = %d, want %d", capBytes, int64(2<<30))
	}
	cases := []struct {
		name, project string
		want          bool
	}{
		{"dune_timescaledb_data", "", true}, // glob
		{"prod-db", "", true},               // exact name
		{"prod-db-2", "", false},            // glob is not prefix match
		{"anon123", "osprey", true},         // project
		{"anon123", "pebble", false},        // other project
	}
	for _, tc := range cases {
		if got := c.Docker.Keeps(tc.name, tc.project); got != tc.want {
			t.Errorf("Keeps(%q, %q) = %v, want %v", tc.name, tc.project, got, tc.want)
		}
	}
}

func TestLoadFileUnknownKeyFails(t *testing.T) {
	p := writeConfig(t, "docker:\n  kep: [x]\n")
	if _, err := LoadFile(p); err == nil {
		t.Fatal("typo'd key must fail, not silently mean no config")
	}
}

func TestLoadFileBadEntries(t *testing.T) {
	for _, body := range []string{
		"docker:\n  keep:\n    - {}\n",               // empty entry
		"docker:\n  keep:\n    - name: \"[bad\"\n",   // broken glob
		"docker:\n  volume_export_cap: 10 parsecs\n", // unknown unit
	} {
		p := writeConfig(t, body)
		if _, err := LoadFile(p); err == nil {
			t.Errorf("config %q must fail validation", strings.TrimSpace(body))
		}
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"10GiB", 10 << 30},
		{"1.5GB", 1500000000},
		{"512MiB", 512 << 20},
		{"250kB", 250000},
		{"0", 0},
		{"1024", 1024},
		{"2 GiB", 2 << 30},
	}
	for _, tc := range cases {
		got, err := ParseSize(tc.in)
		if err != nil {
			t.Errorf("ParseSize(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"", "GB", "ten GB", "-5GB"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) must fail", bad)
		}
	}
}
