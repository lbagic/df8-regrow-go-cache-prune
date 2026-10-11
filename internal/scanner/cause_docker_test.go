package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

func TestDockerVMMemory(t *testing.T) {
	const store, legacy = "settings-store.json", "settings.json"
	tests := []struct {
		name   string
		files  map[string]string
		locked string
		want   engine.Verdict
		detail []string
	}{
		{
			name: "no Docker Desktop",
			want: engine.VerdictNormal, detail: []string{"does not apply"},
		},
		{
			name:  "settings without a memory key",
			files: map[string]string{store: `{"AutoStart": false, "SettingsVersion": 43}`},
			want:  engine.VerdictFlagged, detail: []string{"no memory limit", "half of this Mac's memory (8.0 GiB of 16.0 GiB)"},
		},
		{
			name:  "limit set",
			files: map[string]string{store: `{"AutoStart": false, "MemoryMiB": 4096}`},
			want:  engine.VerdictNormal, detail: []string{"capped at 4.0 GiB of this Mac's 16.0 GiB"},
		},
		{
			name:  "limit set in the older settings file",
			files: map[string]string{legacy: `{"memoryMiB": 2048, "cpus": 4}`},
			want:  engine.VerdictNormal, detail: []string{"capped at 2.0 GiB"},
		},
		{
			name:  "a limit just under half the memory",
			files: map[string]string{store: `{"MemoryMiB": 6144}`},
			want:  engine.VerdictNormal, detail: []string{"capped at 6.0 GiB of this Mac's 16.0 GiB"},
		},
		{
			name:  "a limit at the default is no cap",
			files: map[string]string{store: `{"MemoryMiB": 8192}`},
			want:  engine.VerdictFlagged, detail: []string{"the memory limit is 8.0 GiB, half or more of this Mac's 16.0 GiB"},
		},
		{
			name:  "the default written out by the older settings file",
			files: map[string]string{legacy: `{"memoryMiB": 8192, "cpus": 4}`},
			want:  engine.VerdictFlagged, detail: []string{"the memory limit is 8.0 GiB, half or more of this Mac's 16.0 GiB"},
		},
		{
			name:  "a limit above the default",
			files: map[string]string{store: `{"MemoryMiB": 12288}`},
			want:  engine.VerdictFlagged, detail: []string{"the memory limit is 12.0 GiB, half or more of this Mac's 16.0 GiB"},
		},
		{
			name:  "the newer file wins over a leftover older one",
			files: map[string]string{store: `{"AutoStart": true}`, legacy: `{"memoryMiB": 2048}`},
			want:  engine.VerdictFlagged, detail: []string{"no memory limit"},
		},
		{
			name:  "zero is no limit",
			files: map[string]string{store: `{"MemoryMiB": 0}`},
			want:  engine.VerdictFlagged, detail: []string{"no memory limit"},
		},
		{
			name:  "not JSON",
			files: map[string]string{store: `{"AutoStart": fal`},
			want:  engine.VerdictUnknown, detail: []string{"settings-store.json could not be read as Docker Desktop's settings"},
		},
		{
			name:  "an empty file",
			files: map[string]string{store: ``},
			want:  engine.VerdictUnknown, detail: []string{"settings-store.json could not be read as Docker Desktop's settings"},
		},
		{
			name:  "a memory value that is not a number",
			files: map[string]string{store: `{"MemoryMiB": "lots"}`},
			want:  engine.VerdictUnknown, detail: []string{"MemoryMiB", "not a number"},
		},
		{
			name:   "unreadable",
			files:  map[string]string{store: `{"MemoryMiB": 4096}`},
			locked: store,
			want:   engine.VerdictUnknown, detail: []string{"~/Library/Group Containers/group.com.docker/settings-store.json", "Full Disk Access may be needed"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.locked != "" && os.Geteuid() == 0 {
				t.Skip("root reads mode-000 files")
			}
			c := causeFixture(t)
			dir := filepath.Join(c.host.Home, "Library/Group Containers/group.com.docker")
			for name, text := range tt.files {
				writeText(t, filepath.Join(dir, name), text)
			}
			if tt.locked != "" {
				chmod(t, filepath.Join(dir, tt.locked), 0)
			}
			verdict, detail := c.dockerVMMemory(context.Background(), engine.Rule{})
			wantCause(t, verdict, detail, tt.want, tt.detail...)
		})
	}
}

func TestDockerVMMemoryWithoutAHostFigure(t *testing.T) {
	c := causeFixture(t)
	c.memory = func() int64 { return 0 }
	writeText(t, filepath.Join(c.host.Home, "Library/Group Containers/group.com.docker/settings-store.json"), `{}`)
	verdict, detail := c.dockerVMMemory(context.Background(), engine.Rule{})
	wantCause(t, verdict, detail, engine.VerdictFlagged, "half of this Mac's memory")
	if detail[len(detail)-1] == ')' {
		t.Errorf("detail %q must not print a figure it does not have", detail)
	}

	// With nothing to compare a limit against, it reads as the cap it
	// was set as.
	writeText(t, filepath.Join(c.host.Home, "Library/Group Containers/group.com.docker/settings-store.json"), `{"MemoryMiB": 8192}`)
	verdict, detail = c.dockerVMMemory(context.Background(), engine.Rule{})
	wantCause(t, verdict, detail, engine.VerdictNormal, "capped at 8.0 GiB")
}

// What each config means is what Docker Engine 29.8 does with it:
// collection is on unless enabled is false, a policy replaces the
// default sizes, and defaultKeepStorage is the older name of
// defaultReservedSpace.
func TestDockerBuildCacheLimit(t *testing.T) {
	const unset = "builder.gc.enabled is not set, which means on since Docker Engine 28.2"
	tests := []struct {
		name    string
		config  string
		want    engine.Verdict
		detail  []string
		without string
	}{
		{
			name:    "Docker Desktop's stock config",
			config:  `{"builder": {"gc": {"defaultKeepStorage": "20GB", "enabled": true}}, "experimental": false}`,
			want:    engine.VerdictNormal,
			detail:  []string{"garbage collection on: builder.gc.defaultKeepStorage is 20GB"},
			without: "not set",
		},
		{
			name:   "sized by the current keys",
			config: `{"builder": {"gc": {"enabled": true, "defaultReservedSpace": "10GB", "defaultMaxUsedSpace": "30GB"}}}`,
			want:   engine.VerdictNormal,
			detail: []string{"builder.gc.defaultReservedSpace is 10GB, builder.gc.defaultMaxUsedSpace is 30GB"},
		},
		{
			name:    "the newer size name wins over the older",
			config:  `{"builder": {"gc": {"enabled": true, "defaultKeepStorage": "20GB", "defaultReservedSpace": "5GB"}}}`,
			want:    engine.VerdictNormal,
			detail:  []string{"builder.gc.defaultReservedSpace is 5GB"},
			without: "defaultKeepStorage",
		},
		{
			name:    "collection on, no size",
			config:  `{"builder": {"gc": {"enabled": true}}}`,
			want:    engine.VerdictNormal,
			detail:  []string{"Docker's default limits"},
			without: "not set",
		},
		{
			name:    "a policy replaces the default sizes",
			config:  `{"builder": {"gc": {"enabled": true, "defaultKeepStorage": "20GB", "policy": [{"keepStorage": "10GB", "all": true}]}}}`,
			want:    engine.VerdictNormal,
			detail:  []string{"under the policy in builder.gc.policy"},
			without: "defaultKeepStorage",
		},
		{
			name:   "no builder block: on by default",
			config: `{"experimental": false}`,
			want:   engine.VerdictNormal,
			detail: []string{"Docker's default limits", unset},
		},
		{
			name:   "a size without the switch: on by default, under that size",
			config: `{"builder": {"gc": {"defaultKeepStorage": "20GB"}}}`,
			want:   engine.VerdictNormal,
			detail: []string{"builder.gc.defaultKeepStorage is 20GB", unset},
		},
		{
			name:   "collection switched off, size left behind",
			config: `{"builder": {"gc": {"defaultKeepStorage": "20GB", "enabled": false}}}`,
			want:   engine.VerdictFlagged,
			detail: []string{"~/.docker/daemon.json switches build-cache garbage collection off", "nothing limits the cache"},
		},
		{
			name:   "a policy with no rule collects nothing",
			config: `{"builder": {"gc": {"enabled": true, "policy": []}}}`,
			want:   engine.VerdictFlagged,
			detail: []string{"a policy with no rule", "nothing limits the cache"},
		},
		{
			name:   "a null policy is no policy",
			config: `{"builder": {"gc": {"enabled": true, "policy": null, "defaultReservedSpace": "20GB"}}}`,
			want:   engine.VerdictNormal,
			detail: []string{"builder.gc.defaultReservedSpace is 20GB"},
		},
		{
			name:   "not JSON",
			config: `builder: gc`,
			want:   engine.VerdictUnknown,
			detail: []string{"~/.docker/daemon.json could not be read as Docker Engine's settings"},
		},
		{
			name:   "an object with a field of the wrong type",
			config: `{"builder": {"gc": {"enabled": "yes"}}}`,
			want:   engine.VerdictUnknown,
			detail: []string{"~/.docker/daemon.json could not be read as Docker Engine's settings"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := causeFixture(t)
			writeText(t, filepath.Join(c.host.Home, ".docker/daemon.json"), tt.config)
			verdict, detail := c.dockerBuildCacheLimit(context.Background(), engine.Rule{})
			wantCause(t, verdict, detail, tt.want, tt.detail...)
			if tt.without != "" && strings.Contains(detail, tt.without) {
				t.Errorf("detail %q must not say %q", detail, tt.without)
			}
		})
	}

	t.Run("no daemon.json", func(t *testing.T) {
		c := causeFixture(t)
		verdict, detail := c.dockerBuildCacheLimit(context.Background(), engine.Rule{})
		wantCause(t, verdict, detail, engine.VerdictNormal, "does not apply: no ~/.docker/daemon.json")
	})
}
