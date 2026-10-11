package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

// plantRepo lays out a main checkout at repo with n linked worktrees
// under <repo>/.wt, registered the way git registers them. mod is
// where each checkout keeps its go.mod; "" plants none.
func plantRepo(t *testing.T, repo, mod string, n int) {
	t.Helper()
	writeText(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeText(t, filepath.Join(repo, "README.md"), "fixture\n")
	if mod != "" {
		writeText(t, filepath.Join(repo, mod), "module example.com/svc\n")
	}
	if n == 0 {
		if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for i := range n {
		name := fmt.Sprintf("wt%d", i)
		plantWorktree(t, repo, name, filepath.Join(repo, ".wt", name), mod)
	}
}

// plantWorktree registers a linked worktree of repo checked out at dir.
func plantWorktree(t *testing.T, repo, name, dir, mod string) {
	t.Helper()
	admin := filepath.Join(repo, ".git", "worktrees", name)
	writeText(t, filepath.Join(admin, "gitdir"), filepath.Join(dir, ".git")+"\n")
	writeText(t, filepath.Join(dir, ".git"), "gitdir: "+admin+"\n")
	if mod != "" {
		writeText(t, filepath.Join(dir, mod), "module example.com/svc\n")
	}
}

func TestGoWorktreesTrimpath(t *testing.T) {
	tests := []struct {
		name   string
		plant  func(t *testing.T, home string)
		want   engine.Verdict
		detail []string
	}{
		{
			name:  "three worktrees of a Go module",
			plant: func(t *testing.T, home string) { plantRepo(t, filepath.Join(home, "workspace/org/svc"), "go.mod", 3) },
			want:  engine.VerdictFlagged, detail: []string{"~/workspace/org/svc has 3 worktrees of one Go module; GOFLAGS has no -trimpath"},
		},
		{
			name:  "two worktrees are not many",
			plant: func(t *testing.T, home string) { plantRepo(t, filepath.Join(home, "workspace/org/svc"), "go.mod", 2) },
			want:  engine.VerdictNormal, detail: []string{"no Go module with 3 or more worktrees under ~/workspace"},
		},
		{
			name: "a registration whose checkout is gone does not count",
			plant: func(t *testing.T, home string) {
				repo := filepath.Join(home, "workspace/org/svc")
				plantRepo(t, repo, "go.mod", 3)
				if err := os.RemoveAll(filepath.Join(repo, ".wt", "wt1")); err != nil {
					t.Fatal(err)
				}
			},
			want: engine.VerdictNormal, detail: []string{"no Go module"},
		},
		{
			name:  "many worktrees of a repository without Go",
			plant: func(t *testing.T, home string) { plantRepo(t, filepath.Join(home, "workspace/org/web"), "", 5) },
			want:  engine.VerdictNormal, detail: []string{"no Go module"},
		},
		{
			name: "the module one folder down",
			plant: func(t *testing.T, home string) {
				plantRepo(t, filepath.Join(home, "workspace/org/mono"), "backend/go.mod", 4)
			},
			want: engine.VerdictFlagged, detail: []string{"~/workspace/org/mono has 4 worktrees"},
		},
		{
			name: "worktrees checked out elsewhere, one by a relative link",
			plant: func(t *testing.T, home string) {
				repo := filepath.Join(home, "code/svc")
				plantRepo(t, repo, "go.mod", 1)
				elsewhere := filepath.Join(filepath.Dir(filepath.Dir(home)), "scratch")
				plantWorktree(t, repo, "far", filepath.Join(elsewhere, "far"), "go.mod")
				plantWorktree(t, repo, "rel", filepath.Join(repo, ".wt", "rel"), "go.mod")
				writeText(t, filepath.Join(repo, ".git/worktrees/rel/gitdir"), "../../../.wt/rel/.git\n")
			},
			want: engine.VerdictFlagged, detail: []string{"~/code/svc has 3 worktrees"},
		},
		{
			name: "the repository with the most worktrees is named",
			plant: func(t *testing.T, home string) {
				plantRepo(t, filepath.Join(home, "workspace/a/small"), "go.mod", 3)
				plantRepo(t, filepath.Join(home, "workspace/b/big"), "go.mod", 6)
				plantRepo(t, filepath.Join(home, "workspace/c/web"), "", 9)
			},
			want: engine.VerdictFlagged, detail: []string{"~/workspace/b/big has 6 worktrees of one Go module (2 repositories with 3 or more)"},
		},
		{
			name:  "a repository outside the project roots is not looked for",
			plant: func(t *testing.T, home string) { plantRepo(t, filepath.Join(home, "Documents/svc"), "go.mod", 5) },
			want:  engine.VerdictNormal, detail: []string{"no Go module"},
		},
		{
			name:  "no project folders at all",
			plant: func(*testing.T, string) {},
			want:  engine.VerdictNormal, detail: []string{"no Go module"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := causeFixture(t)
			tt.plant(t, c.host.Home)
			verdict, detail := c.goWorktreesTrimpath(context.Background(), engine.Rule{})
			wantCause(t, verdict, detail, tt.want, tt.detail...)
		})
	}
}

func TestGoWorktreesTrimpathReadsGOFLAGS(t *testing.T) {
	tests := []struct {
		name   string
		flags  string
		err    error
		want   engine.Verdict
		detail string
	}{
		{"unset", "", nil, engine.VerdictFlagged, "GOFLAGS has no -trimpath"},
		{"other flags only", "-mod=mod -tags=trimpath", nil, engine.VerdictFlagged, "GOFLAGS has no -trimpath"},
		{"set", "-mod=mod -trimpath", nil, engine.VerdictNormal, "GOFLAGS has -trimpath"},
		{"switched off again", "-trimpath -trimpath=false", nil, engine.VerdictFlagged, "GOFLAGS has no -trimpath"},
		{"no go command", "", &exec.Error{Name: "go", Err: exec.ErrNotFound}, engine.VerdictNormal, "does not apply: no go command on PATH"},
		{"go env fails", "", errors.New("exit status 2"), engine.VerdictUnknown, "`go env GOFLAGS` failed: exit status 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := causeFixture(t)
			plantRepo(t, filepath.Join(c.host.Home, "workspace/org/svc"), "go.mod", 3)
			var asked []string
			c.goEnv = func(_ context.Context, key string) (string, error) {
				asked = append(asked, key)
				return tt.flags, tt.err
			}
			verdict, detail := c.goWorktreesTrimpath(context.Background(), engine.Rule{})
			wantCause(t, verdict, detail, tt.want, tt.detail)
			if len(asked) != 1 || asked[0] != "GOFLAGS" {
				t.Errorf("asked go env for %v, want GOFLAGS once", asked)
			}
		})
	}
}

func TestGoWorktreesUnreadableProjectFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads mode-000 folders")
	}
	c := causeFixture(t)
	plantRepo(t, filepath.Join(c.host.Home, "workspace/org/svc"), "go.mod", 3)
	chmod(t, filepath.Join(c.host.Home, "workspace/org"), 0)
	verdict, detail := c.goWorktreesTrimpath(context.Background(), engine.Rule{})
	wantCause(t, verdict, detail, engine.VerdictUnknown, "no Go module", "Full Disk Access may be needed")
}

func TestHasTrimpath(t *testing.T) {
	for flags, want := range map[string]bool{
		"":                          false,
		"-trimpath":                 true,
		"--trimpath":                true,
		"-mod=mod -trimpath -v":     true,
		"-trimpath=true":            true,
		"-trimpath=false":           false,
		"-trimpath=0 -trimpath":     true,
		"-tags=trimpath":            false,
		"-ldflags=-trimpath":        false,
		"trimpath":                  false,
		"-gcflags=all=-trimpath=/x": false,
	} {
		if got := hasTrimpath(flags); got != want {
			t.Errorf("hasTrimpath(%q) = %v, want %v", flags, got, want)
		}
	}
}
