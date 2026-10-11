package scanner

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/config"
	"github.com/lbagic/regrow/internal/docker"
	"github.com/lbagic/regrow/internal/engine"
)

// TestDockerImagesUnusedRule pins what keeps the rule out of a default
// clean and its removal to one image, unforced. It executes nothing.
func TestDockerImagesUnusedRule(t *testing.T) {
	r := embeddedRule(t, "docker-images-unused")
	if !r.Beta {
		t.Error("docker-images-unused must ship behind --beta-rules")
	}
	if r.Risk != engine.RiskCaution {
		t.Errorf("risk = %q, want caution: never pre-selected", r.Risk)
	}
	got, err := r.NativeCommand.ExpandItem(engine.Item{Arg: "c0ffee123456"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"docker", "image", "rm", "c0ffee123456"}; !reflect.DeepEqual(got, want) {
		t.Errorf("rule command = %v, want %v", got, want)
	}
}

// Each image rule reads its own tier. Wired the other way round, the
// safe rule would offer named images in a default clean.
func TestDockerImageRulesReadTheirOwnTier(t *testing.T) {
	const untagged, named = "aaa111000000", "ddd444000000"
	id := func(short string) string { return "sha256:" + short + strings.Repeat("0", 64-len(short)) }
	run := func(_ context.Context, args ...string) ([]byte, bool, error) {
		switch strings.Join(args[:2], " ") {
		case "system df":
			return []byte(`{"Images":[` +
				`{"Containers":"0","CreatedAt":"2026-04-02 12:00:00 +0000 UTC","ID":"` + id(untagged) + `","Repository":"<none>","Tag":"<none>","UniqueSize":"500MB"},` +
				`{"Containers":"0","CreatedAt":"2026-04-02 12:00:00 +0000 UTC","ID":"` + id(named) + `","Repository":"registry.example.com/acme/api","Tag":"1.4","UniqueSize":"847.1MB"}]}`), true, nil
		case "image inspect":
			return []byte(`[` +
				`{"Id":"` + id(untagged) + `","RepoTags":[],"Metadata":{"LastTagTime":"2026-06-01T08:00:00Z"}},` +
				`{"Id":"` + id(named) + `","RepoTags":["registry.example.com/acme/api:1.4"],"Metadata":{"LastTagTime":"2026-06-01T08:00:00Z"},` +
				`"Identity":{"Pull":[{"Repository":"registry.example.com/acme/api"}]}}]`), true, nil
		}
		t.Fatalf("unexpected docker call: %v", args)
		return nil, false, nil
	}
	queries := dockerQueriesOf(&docker.Provider{
		Exec:       run,
		LedgerPath: filepath.Join(t.TempDir(), "ledger.json"),
		LoadConfig: func() (config.Docker, error) { return config.Docker{}, nil },
		Now:        func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) },
	})
	for ruleID, want := range map[string]string{
		"docker-images-dangling": untagged,
		"docker-images-unused":   named,
	} {
		items, err := queries[embeddedRule(t, ruleID).ToolQuery](context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].Arg != want {
			t.Errorf("%s offers %+v, want only image %s", ruleID, items, want)
		}
	}
}
