package docker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/trash"
)

func volumeExists(context.Context, ...string) ([]byte, bool, error) {
	return []byte("[{}]"), true, nil
}

func TestExporterWritesTarballAndReceipt(t *testing.T) {
	staging := filepath.Join(t.TempDir(), "staging", "run-1")
	var gotArgs []string
	e := &Exporter{
		StagingDir: staging,
		CapBytes:   10 << 30,
		Exec:       volumeExists,
		Stream: func(_ context.Context, args []string, stdout io.Writer) error {
			gotArgs = args
			_, err := stdout.Write([]byte("TARDATA"))
			return err
		},
	}
	r, err := e.PreAction(context.Background(), engine.Action{
		ItemKey: "dune_timescaledb_data", Bytes: 567900000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != trash.MethodExport || r.Restorable() {
		t.Fatalf("receipt must be a non-restorable export, got %+v", r)
	}
	data, err := os.ReadFile(r.To)
	if err != nil || string(data) != "TARDATA" {
		t.Fatalf("tarball at %s: %q, %v", r.To, data, err)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "dune_timescaledb_data:/volume:ro") {
		t.Errorf("export must mount the volume read-only, got %q", joined)
	}
}

func TestExporterRefusesOverCap(t *testing.T) {
	e := &Exporter{StagingDir: t.TempDir(), CapBytes: 1 << 20, Exec: volumeExists}
	_, err := e.PreAction(context.Background(), engine.Action{ItemKey: "big", Bytes: 2 << 20})
	if err == nil || !strings.Contains(err.Error(), "export cap") {
		t.Fatalf("want cap refusal, got %v", err)
	}
}

func TestExporterRefusesVanishedVolume(t *testing.T) {
	e := &Exporter{
		StagingDir: t.TempDir(),
		Exec: func(context.Context, ...string) ([]byte, bool, error) {
			return nil, true, errors.New("no such volume")
		},
	}
	_, err := e.PreAction(context.Background(), engine.Action{ItemKey: "gone", Bytes: 1})
	if err == nil || !strings.Contains(err.Error(), "vanished") {
		t.Fatalf("want vanished-volume refusal, got %v", err)
	}
}

func TestExporterFailedStreamRemovesPartialTarball(t *testing.T) {
	staging := t.TempDir()
	e := &Exporter{
		StagingDir: staging,
		Exec:       volumeExists,
		Stream: func(_ context.Context, _ []string, stdout io.Writer) error {
			_, _ = stdout.Write([]byte("HALF"))
			return errors.New("daemon died")
		},
	}
	_, err := e.PreAction(context.Background(), engine.Action{ItemKey: "vol", Bytes: 1})
	if err == nil || !strings.Contains(err.Error(), "refusing to remove") {
		t.Fatalf("want export failure, got %v", err)
	}
	entries, _ := os.ReadDir(staging)
	if len(entries) != 0 {
		t.Fatalf("partial tarball must be removed, staging holds %v", entries)
	}
}

func TestExporterRequiresItemKey(t *testing.T) {
	e := &Exporter{StagingDir: t.TempDir(), Exec: volumeExists}
	if _, err := e.PreAction(context.Background(), engine.Action{}); err == nil {
		t.Fatal("empty item key must refuse")
	}
}
