package docker

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lbagic/regrow/internal/config"
	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/oplog"
)

// staleAge is the "old enough to offer" threshold for anonymous
// volumes, stopped containers, unused images and build cache. The
// build-cache rule's `--filter unused-for=720h` is this same 30 days —
// keep them in sync.
const staleAge = 30 * 24 * time.Hour

// Provider owns one scan's view of the daemon: the snapshot is loaded
// once (rules scan concurrently) and every docker tool query reads
// from the same classification. Zero value works; fields exist so
// tests can inject a fake CLI, ledger path, config and clock.
type Provider struct {
	Exec       Exec                          // nil = real docker CLI
	LedgerPath string                        // "" = StateDir()/docker-volumes.json
	LoadConfig func() (config.Docker, error) // nil = config.Load
	Now        func() time.Time              // nil = time.Now

	mu    sync.Mutex
	done  bool
	tiers tiers
	err   error
}

// tiers holds the classified items, one slice per tool query.
type tiers struct {
	volumesNamed      []engine.Item
	volumesAnon       []engine.Item
	volumesKept       []engine.Item
	containersStopped []engine.Item
	imagesDangling    []engine.Item
	imagesUnused      []engine.Item
	buildCache        []engine.Item
}

// Tool queries, one per rule. A nil-snapshot daemon (docker not
// installed, daemon down) yields empty results and no error.

func (p *Provider) VolumesNamed(ctx context.Context) ([]engine.Item, error) {
	t, err := p.load(ctx)
	return t.volumesNamed, err
}

func (p *Provider) VolumesAnon(ctx context.Context) ([]engine.Item, error) {
	t, err := p.load(ctx)
	return t.volumesAnon, err
}

func (p *Provider) VolumesKept(ctx context.Context) ([]engine.Item, error) {
	t, err := p.load(ctx)
	return t.volumesKept, err
}

func (p *Provider) ContainersStopped(ctx context.Context) ([]engine.Item, error) {
	t, err := p.load(ctx)
	return t.containersStopped, err
}

func (p *Provider) ImagesDangling(ctx context.Context) ([]engine.Item, error) {
	t, err := p.load(ctx)
	return t.imagesDangling, err
}

func (p *Provider) ImagesUnused(ctx context.Context) ([]engine.Item, error) {
	t, err := p.load(ctx)
	return t.imagesUnused, err
}

func (p *Provider) BuildCache(ctx context.Context) ([]engine.Item, error) {
	t, err := p.load(ctx)
	return t.buildCache, err
}

// load memoizes the whole pipeline: probe → snapshot → ledger merge →
// classify. One CLI conversation per scan, not per rule.
func (p *Provider) load(ctx context.Context) (tiers, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return p.tiers, p.err
	}
	p.done = true
	p.tiers, p.err = p.loadLocked(ctx)
	return p.tiers, p.err
}

func (p *Provider) loadLocked(ctx context.Context) (tiers, error) {
	loadConfig := p.LoadConfig
	if loadConfig == nil {
		loadConfig = func() (config.Docker, error) {
			c, err := config.Load()
			return c.Docker, err
		}
	}
	cfg, err := loadConfig()
	if err != nil {
		return tiers{}, err
	}
	capBytes, err := cfg.ExportCapBytes()
	if err != nil {
		return tiers{}, err
	}

	run := p.Exec
	if run == nil {
		run = realExec
	}
	snap, err := loadSnapshot(ctx, run)
	if err != nil || snap == nil {
		return tiers{}, err
	}

	ledgerPath := p.LedgerPath
	if ledgerPath == "" {
		dir, err := oplog.StateDir()
		if err != nil {
			return tiers{}, err
		}
		ledgerPath = filepath.Join(dir, "docker-volumes.json")
	}
	led, err := loadLedger(ledgerPath)
	if err != nil {
		return tiers{}, err
	}
	if err := saveLedger(ledgerPath, led.merge(snap)); err != nil {
		return tiers{}, fmt.Errorf("usage ledger: %w", err)
	}

	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	return classify(snap, cfg, capBytes, now()), nil
}

// classify sorts every object into its tier (research §4). The one
// direction this must never fail in: anything referenced, kept, too
// big to back up, or too young lands in the surface-only kept tier —
// visible with a reason, never deletable.
func classify(snap *Snapshot, cfg config.Docker, capBytes int64, now time.Time) tiers {
	staleBefore := now.Add(-staleAge)
	var t tiers

	volumes := append([]Volume(nil), snap.Volumes...)
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].Name < volumes[j].Name })
	for _, v := range volumes {
		item := engine.Item{Label: volumeLabel(v), Arg: v.Name, Bytes: v.Bytes, LastUsed: v.LastUsed}
		kept := func(reason string) {
			item.Label += " — " + reason
			t.volumesKept = append(t.volumesKept, item)
		}
		lastActivity := v.LastUsed
		if lastActivity.IsZero() {
			lastActivity = v.CreatedAt
		}
		switch {
		case len(v.UsedBy) > 0:
			kept("in use by " + v.UsedBy[0])
		case cfg.Keeps(v.Name, v.Project()):
			kept("keep-list")
		case v.Bytes > capBytes:
			kept(fmt.Sprintf("bigger than the %s export cap, delete manually", humanGiB(capBytes)))
		case !v.Anonymous():
			t.volumesNamed = append(t.volumesNamed, item)
		case lastActivity.After(staleBefore):
			kept("anonymous but used within 30d")
		default:
			t.volumesAnon = append(t.volumesAnon, item)
		}
	}

	containers := append([]Container(nil), snap.Containers...)
	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	for _, c := range containers {
		if !c.Stopped() || c.LastUsed().After(staleBefore) || cfg.Keeps(c.Name, c.Project()) {
			continue
		}
		t.containersStopped = append(t.containersStopped, engine.Item{
			Label:    c.Name + " — " + c.Image,
			Arg:      shortID(c.ID),
			Bytes:    c.Bytes,
			LastUsed: c.LastUsed(),
		})
	}

	images := append([]Image(nil), snap.Images...)
	sort.Slice(images, func(i, j int) bool { return images[i].ID < images[j].ID })
	// An unused image is in exactly one of the two image tiers, or in
	// neither.
	for _, im := range images {
		switch {
		case im.InUse():
		case im.Dangling():
			lastUsed := im.LastTagTime
			if lastUsed.IsZero() {
				lastUsed = im.CreatedAt
			}
			t.imagesDangling = append(t.imagesDangling, engine.Item{
				Label:    shortID(im.ID) + " (dangling)",
				Arg:      shortID(im.ID),
				Bytes:    im.Bytes,
				LastUsed: lastUsed,
			})
		case keepsImage(cfg, im):
		case offerUnusedImage(im, staleBefore):
			t.imagesUnused = append(t.imagesUnused, engine.Item{
				Label:    imageName(im),
				Arg:      shortID(im.ID),
				Bytes:    im.Bytes,
				LastUsed: im.LastTagTime,
			})
		}
	}

	// A shared record's bytes are an image layer's: pruning the record
	// frees nothing while the image stays, and `docker system df`
	// leaves it out of RECLAIMABLE for the same reason.
	var cacheBytes int64
	var cacheNewest time.Time
	for _, bc := range snap.BuildCache {
		if bc.InUse || bc.Shared || bc.LastUsedAt.IsZero() || bc.LastUsedAt.After(staleBefore) {
			continue
		}
		cacheBytes += bc.Bytes
		if bc.LastUsedAt.After(cacheNewest) {
			cacheNewest = bc.LastUsedAt
		}
	}
	if cacheBytes > 0 {
		t.buildCache = []engine.Item{{
			Label:    "build cache unused for 30d+",
			Bytes:    cacheBytes,
			LastUsed: cacheNewest,
		}}
	}
	return t
}

// offerUnusedImage decides whether a named image no container uses
// goes into the unused tier. Its age is the time since it was last
// pulled or tagged here, never the image's Created, which is build
// time and can predate the pull by months.
func offerUnusedImage(im Image, staleBefore time.Time) bool {
	switch {
	case im.Built:
		// An image built here stays out, pushed or not
		// (TestUnusedImagesTier).
		return false
	case len(im.Tags) != 1:
		// Unforced, `docker image rm <id>` refuses an image with a
		// second tag or a reference in a second repository.
		return false
	case strings.Contains(im.Tags[0], "@"):
		// Known only by a digest reference: not a tagged image.
		return false
	case !pulledAs(im):
		// Its repository is not where docker records its content came
		// from or went to: a retag into another repository, or a build
		// docker kept no record of.
		return false
	case im.LastTagTime.IsZero():
		// An unknown age must not read as old.
		return false
	}
	return !im.LastTagTime.After(staleBefore)
}

// keepsImage reports whether a keep entry names the image, by a
// repository or by repository:tag, in the row's spelling or in any
// spelling docker reads as one of the image's names. A registry's port
// is held only as the df row shows it: repository drops it from every
// RepoTags entry.
func keepsImage(cfg config.Docker, im Image) bool {
	if cfg.Keeps(im.Repository, "") || cfg.Keeps(imageName(im), "") {
		return true
	}
	for _, ref := range im.Tags {
		_, tag := splitTag(ref)
		for _, repo := range spellings(repository(ref)) {
			if cfg.Keeps(repo, "") || (tag != "" && cfg.Keeps(repo+":"+tag, "")) {
				return true
			}
		}
	}
	return false
}

// pulledAs reports whether the image's one tag names a repository
// docker records its content as pulled from or pushed to.
func pulledAs(im Image) bool {
	host, path := repository(im.Tags[0])
	for _, origin := range im.PulledFrom {
		if h, p := repository(origin); h == host && p == path {
			return true
		}
	}
	return false
}

// imageName is the image's first tag as `system df -v` shows it.
func imageName(im Image) string {
	if im.Tag == "" || im.Tag == "<none>" {
		return im.Repository
	}
	return im.Repository + ":" + im.Tag
}

// volumeLabel names a volume for humans: compose project when known,
// a short handle for anonymous 64-hex names.
func volumeLabel(v Volume) string {
	name := v.Name
	if v.Anonymous() {
		name = shortID(v.Name) + " (anonymous)"
	}
	if project := v.Project(); project != "" {
		name += " — project " + project
	}
	return name
}

// shortID is docker's usual 12-char handle.
func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func humanGiB(b int64) string {
	return fmt.Sprintf("%g GiB", float64(b)/(1<<30))
}
