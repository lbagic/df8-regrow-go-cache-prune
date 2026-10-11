package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

// finderMover reports a move into the Trash without touching the disk.
type finderMover struct{}

func (finderMover) Move(_ context.Context, path string) (trash.Receipt, error) {
	return trash.Receipt{Original: path, To: "/fixture-trash/" + filepath.Base(path), Method: trash.MethodFinder}, nil
}

// stagingMover reports a move into regrow staging, Finder being unavailable.
type stagingMover struct{}

func (stagingMover) Move(_ context.Context, path string) (trash.Receipt, error) {
	return trash.Receipt{Original: path, To: "/fixture-staging/" + filepath.Base(path), Method: trash.MethodStaging}, nil
}

// fakeDocker puts a `docker` on PATH that finds every volume and writes
// a few bytes as its tarball, so the run executor's own volume export
// runs and writes where it really writes.
func fakeDocker(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1 $2\" in\n  \"volume inspect\") echo '[]' ;;\n  \"run --rm\") printf tar ;;\n  *) echo \"unexpected docker call: $*\" >&2; exit 2 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// summaryOf runs actions through the run executor `regrow clean` builds,
// with the Trash, the journal and the commands injected and the volume
// export real over a fake docker, and returns the summary `regrow clean`
// prints for the run, and the staging directory that summary names.
func summaryOf(t *testing.T, mover executor.Mover, actions ...engine.Action) (lines []string, staging string) {
	t.Helper()
	fakeDocker(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	exe, release, err := newRunExecutor(engine.Host{OS: "darwin", Home: t.TempDir()}, "fixture-run", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	logPath, err := oplog.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	staging = runStagingDir(logPath, "fixture-run")
	if got := exe.Trash.(*trash.Mover).StagingDir; got != staging {
		t.Fatalf("the run stages into %s, the summary names %s", got, staging)
	}
	exe.Trash = mover
	exe.Log = &memJournal{}
	exe.RunNative = func(_ context.Context, argv []string) error {
		if argv[len(argv)-1] == "busy_db" {
			return errors.New("volume is in use")
		}
		return nil
	}
	res, err := exe.Execute(context.Background(), engine.Plan{Actions: actions})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actions {
		if a.PreAction != engine.PreActionVolumeExport {
			continue
		}
		if _, err := os.Stat(filepath.Join(staging, "docker-volume-"+a.ItemKey+".tar")); err != nil {
			t.Errorf("the export of %s is not in the folder the summary names: %v", a.ItemKey, err)
		}
	}
	return runSummary(res, staging), staging
}

func volumeRemoval(name string, bytes int64) engine.Action {
	return engine.Action{RuleID: "docker-volumes-named", ItemKey: name, Kind: engine.ActionNative,
		Command: []string{"docker", "volume", "rm", name}, Bytes: bytes, PreAction: engine.PreActionVolumeExport}
}

func folderToTrash(path string, bytes int64) engine.Action {
	return engine.Action{RuleID: "npm-cache", Kind: engine.ActionTrash, Path: path, Bytes: bytes, Command: trash.PreviewCommand(path)}
}

func steward(bytes int64) engine.Action {
	return engine.Action{RuleID: "go-build-cache", Kind: engine.ActionNative, Command: []string{"go", "clean", "-cache"}, Bytes: bytes}
}

// summaryLine is the line that opens with label, or "".
func summaryLine(lines []string, label string) string {
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), label) {
			return l
		}
	}
	return ""
}

func TestRunSummaryCountsAnExportedVolumeInStaging(t *testing.T) {
	tests := []struct {
		name    string
		mover   executor.Mover
		actions []engine.Action
		freed   string
		trash   string
		staged  string // "" = no staging line
		export  string // "" = no export line
	}{
		{"a removed volume is not freed while its tarball is kept", finderMover{},
			[]engine.Action{volumeRemoval("dune_db", 3*gib)}, "0 B", "0 B", "", "3.0 GiB"},
		{"a trashed folder is in the Trash", finderMover{},
			[]engine.Action{folderToTrash("/u/proj/cache", 2*gib)}, "0 B", "2.0 GiB", "", ""},
		{"each figure counts its own actions", finderMover{},
			[]engine.Action{steward(5 * gib), volumeRemoval("dune_db", 3*gib), folderToTrash("/u/proj/cache", 2*gib)}, "5.0 GiB", "2.0 GiB", "", "3.0 GiB"},
		{"a move to staging and a volume export share a folder and stay two figures", stagingMover{},
			[]engine.Action{folderToTrash("/u/proj/cache", 2*gib), volumeRemoval("dune_db", 3*gib)}, "0 B", "0 B", "2.0 GiB", "3.0 GiB"},
		{"a volume whose removal failed is counted nowhere", finderMover{},
			[]engine.Action{volumeRemoval("busy_db", 4*gib)}, "0 B", "0 B", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines, staging := summaryOf(t, tt.mover, tt.actions...)
			out := strings.Join(lines, "\n")
			if l := summaryLine(lines, "Freed now"); !strings.HasSuffix(l, tt.freed) {
				t.Errorf("Freed now = %q, want %s in:\n%s", l, tt.freed, out)
			}
			if l := summaryLine(lines, "In the Trash"); !strings.Contains(l, " "+tt.trash+" ") {
				t.Errorf("In the Trash = %q, want %s in:\n%s", l, tt.trash, out)
			}
			if l := summaryLine(lines, "In regrow staging"); (tt.staged == "") != (l == "") || !strings.Contains(l, tt.staged) {
				t.Errorf("In regrow staging = %q, want %q in:\n%s", l, tt.staged, out)
			}
			l := summaryLine(lines, "Volume exports")
			if tt.export == "" {
				if l != "" {
					t.Errorf("no volume was removed, want no export line in:\n%s", out)
				}
				return
			}
			for _, want := range []string{tt.export, staging, "docker-volume-*.tar", "delete those files", "`regrow undo` does not restore"} {
				if !strings.Contains(l, want) {
					t.Errorf("the export line %q lacks %q", l, want)
				}
			}
			if strings.Contains(l, "delete that folder") || strings.Contains(l, "delete the folder") {
				t.Errorf("the export line %q tells the user to delete the folder, which can also hold moves `regrow undo` restores", l)
			}
			if !strings.Contains(out, "Docker hands the space") {
				t.Errorf("the summary must say Docker's side comes back only when Docker trims its disk image, got:\n%s", out)
			}
		})
	}
}
