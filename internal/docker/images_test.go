package docker

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/config"
	"github.com/lbagic/regrow/internal/engine"
)

// Synthetic CLI output in the shapes docker 29 prints. Against testNow
// (2026-07-13) the 30 days end on 2026-06-13. Every image that must
// stay out differs from the plainly offered one (800000000008) in the
// one respect TestUnusedImagesTier names for it.

func imageID(short string) string {
	return "sha256:" + short + strings.Repeat("0", 64-len(short))
}

func cannedDF() string {
	row := func(short, repo, tag, created, containers, size, shared, unique string) string {
		return `{"Containers":"` + containers + `","CreatedAt":"` + created + `","ID":"` + imageID(short) +
			`","Repository":"` + repo + `","Tag":"` + tag + `","SharedSize":"` + shared + `","Size":"` + size + `","UniqueSize":"` + unique + `"}`
	}
	const built = "2026-04-02 12:00:00 +0000 UTC"
	return `{"Images":[` + strings.Join([]string{
		row("100000000001", "example/web", "3", built, "1", "120MB", "0B", "120MB"),
		row("200000000002", "example/job", "3", built, "1", "90MB", "0B", "90MB"),
		row("300000000003", "<none>", "<none>", built, "0", "500MB", "0B", "500MB"),
		row("400000000004", "acme-local/api", "dev", built, "0", "1.7GB", "0B", "1.701GB"),
		row("e0000000000e", "acme-local/worker", "dev", built, "0", "300MB", "0B", "300MB"),
		// Built two years ago, pulled last week.
		row("500000000005", "example/cache", "7", "2024-07-01 12:00:00 +0000 UTC", "0", "665MB", "0B", "665.6MB"),
		row("600000000006", "example/app", "1", built, "0", "500MB", "300MB", "200MB"),
		row("700000000007", "example/app", "2", built, "0", "500MB", "300MB", "200MB"),
		row("800000000008", "registry.example.com/acme/api", "1.4", built, "0", "847MB", "0B", "847.1MB"),
		row("900000000009", "example/tool", "1", built, "0", "60MB", "0B", "60MB"),
		row("a0000000000a", "example/nocount", "1", built, "N/A", "70MB", "0B", "70MB"),
		row("b0000000000b", "example/ghost", "1", built, "0", "80MB", "0B", "80MB"),
		row("c0000000000c", "example/notime", "1", built, "0", "40MB", "0B", "40MB"),
		row("d0000000000d", "example/gone", "1", built, "0", "30MB", "0B", "30MB"),
		row("f0000000000f", "<none>", "<none>", built, "N/A", "110MB", "0B", "110MB"),
		row("f1000000000f", "<none>", "<none>", built, "0", "120MB", "0B", "120MB"),
		row("f2000000000f", "<none>", "<none>", built, "0", "130MB", "0B", "130MB"),
		row("1a000000001a", "localhost:5000/app", "dev", built, "0", "400MB", "0B", "400MB"),
		row("2a000000002a", "myorg/nginx", "prod", built, "0", "190MB", "0B", "190MB"),
		row("3a000000003a", "localhost:5000/cache", "1", built, "0", "250MB", "0B", "250MB"),
		// Pulled by digest: df prints no name for it.
		row("4a000000004a", "<none>", "<none>", built, "0", "411MB", "0B", "411MB"),
		row("5a000000005a", "example/epoch", "1", built, "0", "20MB", "0B", "20MB"),
		row("6a000000006a", "example/mounted", "1", built, "0", "35MB", "0B", "35MB"),
		row("7a000000007a", "nginx", "1.25", built, "0", "190MB", "0B", "190MB"),
	}, ",") + `],"Containers":[{"ID":"c1","Size":"1kB"},{"ID":"c2","Size":"1kB"},{"ID":"c3","Size":"1kB"},{"ID":"c4","Size":"1kB"},{"ID":"c5","Size":"1kB"}]}`
}

func containerLine(id, name, status, imageShort, mounts string) string {
	return `{"ID":"` + id + `","Name":"/` + name + `","Created":"2026-07-01T00:00:00Z","Status":"` + status +
		`","StartedAt":"2026-07-01T00:00:01Z","FinishedAt":"0001-01-01T00:00:00Z","Mounts":` + mounts + `,"Labels":{},"Image":"` + name +
		`","ImageID":"` + imageID(imageShort) + `"}`
}

// imageMount is a container's .Mounts entry for `--mount type=image,source=<source>`.
func imageMount(source string) string {
	return `[{"Type":"image","Name":"` + source + `","Source":"/var/lib/docker/snapshot","Destination":"/data","RW":false}]`
}

func cannedContainers() string {
	return strings.Join([]string{
		containerLine("c1", "web", "running", "100000000001", "[]"),
		containerLine("c2", "job", "exited", "200000000002", "[]"),
		// df counts no container for these images; the join must.
		containerLine("c3", "ghost", "exited", "b0000000000b", "[]"),
		containerLine("c4", "builder", "exited", "f1000000000f", "[]"),
		containerLine("c5", "reader", "exited", "100000000001", imageMount(imageID("6a000000006a"))),
	}, "\n")
}

func cannedImageInspect() string {
	digest := "@sha256:" + strings.Repeat("f", 64)
	entry := func(short string, tags []string, lastTag, identity string) string {
		digests := []string{}
		if len(tags) > 0 {
			// The containerd image store gives every named image a
			// digest, a locally built one included.
			name, _, _ := strings.Cut(tags[0], "@")
			if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
				name = name[:i]
			}
			digests = []string{name + digest}
		}
		quotedTags, _ := json.Marshal(tags)
		quotedDigests, _ := json.Marshal(digests)
		return `{"Id":"` + imageID(short) + `","RepoTags":` + string(quotedTags) + `,"RepoDigests":` + string(quotedDigests) +
			`,"Created":"2026-04-02T12:00:00Z","Config":{"Env":["PATH=/usr/bin"]},"Metadata":{"LastTagTime":"` + lastTag + `"}` + identity + `}`
	}
	pulled := func(repo string) string { return `,"Identity":{"Pull":[{"Repository":"` + repo + `"}]}` }
	const old = "2026-06-01T08:00:00.123456789Z"
	return `[` + strings.Join([]string{
		entry("100000000001", []string{"example/web:3"}, old, pulled("docker.io/example/web")),
		entry("200000000002", []string{"example/job:3"}, old, pulled("docker.io/example/job")),
		entry("300000000003", nil, old, pulled("docker.io/example/app")),
		entry("400000000004", []string{"acme-local/api:dev"}, old, `,"Identity":null`),
		entry("e0000000000e", []string{"acme-local/worker:dev"}, old, `,"Identity":{"Build":[{"Ref":"abc/def/ghi","CreatedAt":"2026-06-01T08:00:00Z"}]}`),
		entry("500000000005", []string{"example/cache:7"}, "2026-07-06T10:00:00Z", pulled("docker.io/example/cache")),
		entry("600000000006", []string{"example/app:1"}, old, pulled("docker.io/example/app")),
		entry("700000000007", []string{"example/app:2"}, old, pulled("docker.io/example/app")),
		entry("800000000008", []string{"registry.example.com/acme/api:1.4"}, old, pulled("registry.example.com/acme/api")),
		entry("900000000009", []string{"example/tool:1", "example/tool:stable"}, old, pulled("docker.io/example/tool")),
		entry("a0000000000a", []string{"example/nocount:1"}, old, pulled("docker.io/example/nocount")),
		entry("b0000000000b", []string{"example/ghost:1"}, old, pulled("docker.io/example/ghost")),
		entry("c0000000000c", []string{"example/notime:1"}, "0001-01-01T00:00:00Z", pulled("docker.io/example/notime")),
		// d0000000000d and f2000000000f were removed between df and inspect.
		entry("f0000000000f", nil, old, ""),
		entry("f1000000000f", nil, old, ""),
		// Built here, then `docker push localhost:5000/app:dev`: the push
		// adds the repository to Pull, without its port.
		entry("1a000000001a", []string{"localhost:5000/app:dev"}, old,
			`,"Identity":{"Build":[{"Ref":"abc/def/ghi","CreatedAt":"2026-06-01T08:00:00Z"}],"Pull":[{"Repository":"localhost/app"}]}`),
		// `docker pull nginx:1.25; docker tag nginx:1.25 myorg/nginx:prod; docker rmi nginx:1.25`.
		entry("2a000000002a", []string{"myorg/nginx:prod"}, old, pulled("docker.io/library/nginx")),
		entry("3a000000003a", []string{"localhost:5000/cache:1"}, old, pulled("localhost/cache")),
		// `docker pull example/pinned@sha256:…`: the containerd image
		// store lists the digest reference in RepoTags.
		entry("4a000000004a", []string{"example/pinned" + digest}, old, pulled("docker.io/example/pinned")),
		entry("5a000000005a", []string{"example/epoch:1"}, "1970-01-01T00:00:00Z", pulled("docker.io/example/epoch")),
		entry("6a000000006a", []string{"example/mounted:1"}, old, pulled("docker.io/example/mounted")),
		entry("7a000000007a", []string{"nginx:1.25"}, old, pulled("docker.io/library/nginx")),
	}, ",") + `]`
}

func cannedImageTiers(t *testing.T) tiers {
	t.Helper()
	return imageTiers(t, cannedContainers())
}

func imageTiers(t *testing.T, containerLines string) tiers {
	t.Helper()
	var df dfJSON
	if err := json.Unmarshal([]byte(cannedDF()), &df); err != nil {
		t.Fatal(err)
	}
	containers, err := parseContainerLines([]byte(containerLines))
	if err != nil {
		t.Fatal(err)
	}
	inspected, err := parseImageInspect([]byte(cannedImageInspect()))
	if err != nil {
		t.Fatal(err)
	}
	snap := &Snapshot{Containers: containers, Images: joinImages(df.Images, containers, inspected)}
	return classify(snap, config.Docker{}, 0, testNow)
}

func TestUnusedImagesTier(t *testing.T) {
	tr := cannedImageTiers(t)

	offered := map[string]engine.Item{}
	for _, it := range tr.imagesUnused {
		offered[it.Arg] = it
	}
	want := map[string]engine.Item{
		"600000000006": {Label: "example/app:1", Bytes: 200_000_000},
		"700000000007": {Label: "example/app:2", Bytes: 200_000_000},
		"800000000008": {Label: "registry.example.com/acme/api:1.4", Bytes: 847_100_000},
		// Pull names the registry without its port.
		"3a000000003a": {Label: "localhost:5000/cache:1", Bytes: 250_000_000},
		// Pull spells the name in full: docker.io/library/nginx.
		"7a000000007a": {Label: "nginx:1.25", Bytes: 190_000_000},
	}
	for arg, w := range want {
		got, ok := offered[arg]
		if !ok {
			t.Errorf("%s (%s) must be offered", arg, w.Label)
			continue
		}
		if got.Label != w.Label || got.Bytes != w.Bytes {
			t.Errorf("%s = %q, %d bytes; want %q, %d bytes (docker's unique size)", arg, got.Label, got.Bytes, w.Label, w.Bytes)
		}
		// Last-used is the pull time, not the build time of two months earlier.
		if day := got.LastUsed.UTC().Format("2006-01-02"); day != "2026-06-01" {
			t.Errorf("%s LastUsed = %s, want its last tag time 2026-06-01", arg, day)
		}
	}

	for arg, why := range map[string]string{
		"100000000001": "a running container uses it",
		"200000000002": "a stopped container uses it",
		"300000000003": "it is dangling: docker-images-dangling owns it",
		"400000000004": "it was built here: docker recorded no pull, and its digest is not one",
		"e0000000000e": "it was built here: its identity names a build, not a pull",
		"500000000005": "it was pulled a week ago, whatever its build date",
		"900000000009": "it has two tags, which `docker image rm <id>` refuses unforced",
		"a0000000000a": "docker gave no container count for it",
		"b0000000000b": "a stopped container names its ID though df counted none",
		"c0000000000c": "docker reports no tag time, so its age is unknown",
		"d0000000000d": "inspect did not return it",
		"1a000000001a": "it was built here and pushed: the push put its repository in Pull, and its identity still names the build",
		"2a000000002a": "it was retagged: docker pull myorg/nginx:prod cannot bring it back",
		"4a000000004a": "it is known only by a digest reference, not a tag",
		"5a000000005a": "its tag time is the containerd store's Unix-epoch stand-in for none",
		"6a000000006a": "a stopped container mounts it as an image",
	} {
		if it, ok := offered[arg]; ok {
			t.Errorf("%s (%s) must not be offered: %s", arg, it.Label, why)
		}
	}
	if len(offered) != len(want) {
		t.Errorf("offered %d images, want %d: %v", len(offered), len(want), keys(tr.imagesUnused))
	}
}

// Two images share 300 MB of layers: each item is the 200 MB removing
// it alone frees, so the pair sums to 400 MB where plain sizes say 1 GB.
func TestUnusedImagesLeaveSharedLayersOut(t *testing.T) {
	var pair int64
	for _, it := range cannedImageTiers(t).imagesUnused {
		if strings.HasPrefix(it.Label, "example/app:") {
			pair += it.Bytes
		}
	}
	if pair != 400_000_000 {
		t.Errorf("the two images sharing layers sum to %d bytes, want 400000000", pair)
	}
}

func TestImageTiersAreDisjoint(t *testing.T) {
	tr := cannedImageTiers(t)
	if got := keys(tr.imagesDangling); len(got) != 1 || got[0] != "300000000003" {
		t.Fatalf("dangling tier = %v, want only the untagged unused image", got)
	}
	dangling := map[string]bool{}
	for _, it := range tr.imagesDangling {
		dangling[it.Arg] = true
	}
	for _, it := range tr.imagesUnused {
		if dangling[it.Arg] {
			t.Errorf("%s is in both image tiers: its bytes would count twice", it.Arg)
		}
	}
}

// df prints <none> for an image pulled by digest, as it does for an
// untagged leftover: the dangling rule, which is safe and has no age
// test, must not take it.
func TestImagePulledByDigestIsInNeitherTier(t *testing.T) {
	tr := cannedImageTiers(t)
	for tier, items := range map[string][]engine.Item{"dangling": tr.imagesDangling, "unused": tr.imagesUnused} {
		for _, it := range items {
			if it.Arg == "4a000000004a" {
				t.Errorf("the image pulled by digest is in the %s tier as %q", tier, it.Label)
			}
		}
	}
}

func TestDanglingImagesInUseOrUnknownStayOut(t *testing.T) {
	dangling := map[string]bool{}
	for _, it := range cannedImageTiers(t).imagesDangling {
		dangling[it.Arg] = true
	}
	for arg, why := range map[string]string{
		"f0000000000f": "docker gave no container count for it",
		"f1000000000f": "a stopped container names its ID though df counted none",
		"f2000000000f": "inspect did not return it, so nothing says it has no name",
	} {
		if dangling[arg] {
			t.Errorf("dangling image %s must not be offered: %s", arg, why)
		}
	}
}

// df counts no image mounts and an unforced `docker image rm <id>`
// does not check them. A mount by anything but the full ID may have
// resolved to an image whose tag has moved since, so no image is
// offered while such a container exists.
func TestImageMountByReferenceMakesEveryImageInUse(t *testing.T) {
	for _, source := range []string{"example/app:1", "example/app", "6a000000006a", imageID("6a000000006a")[:20]} {
		t.Run(source, func(t *testing.T) {
			lines := cannedContainers() + "\n" + containerLine("c6", "mounter", "exited", "100000000001", imageMount(source))
			tr := imageTiers(t, lines)
			if len(tr.imagesUnused) != 0 || len(tr.imagesDangling) != 0 {
				t.Errorf("unused=%v dangling=%v, want no image offered while a container mounts %q",
					keys(tr.imagesUnused), keys(tr.imagesDangling), source)
			}
		})
	}
	// The same mount by full ID, without "sha256:", leaves the rest offered.
	lines := cannedContainers() + "\n" + containerLine("c6", "mounter", "exited", "100000000001",
		imageMount(strings.TrimPrefix(imageID("600000000006"), "sha256:")))
	got := keys(imageTiers(t, lines).imagesUnused)
	sort.Strings(got)
	if want := "3a000000003a 700000000007 7a000000007a 800000000008"; strings.Join(got, " ") != want {
		t.Errorf("unused tier = %v, want %s: only the mounted image leaves", got, want)
	}
}

// Names compare the way Identity.Pull spells them: normalised, with
// the registry's port dropped.
func TestRepository(t *testing.T) {
	for ref, want := range map[string][2]string{
		"nginx:1.25":                      {"docker.io", "library/nginx"},
		"docker.io/library/nginx":         {"docker.io", "library/nginx"},
		"index.docker.io/library/nginx:1": {"docker.io", "library/nginx"},
		"example/web:3":                   {"docker.io", "example/web"},
		"example/one@sha256:abc":          {"docker.io", "example/one"},
		"ghcr.io/org/img:1.0":             {"ghcr.io", "org/img"},
		"localhost:5000/app:dev":          {"localhost", "app"},
		"localhost/app":                   {"localhost", "app"},
		"registry.example.com:443/a/b:2":  {"registry.example.com", "a/b"},
		"[::1]:5000/app:1":                {"::1", "app"},
		"Registry/app:1":                  {"Registry", "app"},
		// A push to a dotless host with a port records docker.io/registry/app,
		// which this name does not match: the image stays out.
		"registry:5000/app:1": {"registry", "app"},
	} {
		if host, path := repository(ref); host != want[0] || path != want[1] {
			t.Errorf("repository(%q) = %q, %q; want %q, %q", ref, host, path, want[0], want[1])
		}
	}
}

// A keep entry protects an image by its repository or by
// repository:tag, in every spelling docker reads as that name.
func TestKeepListCoversUnusedImages(t *testing.T) {
	image := func(short, repo, tag, origin string) Image {
		return Image{
			ID:          imageID(short),
			Repository:  repo,
			Tag:         tag,
			Tags:        []string{repo + ":" + tag},
			PulledFrom:  []string{origin},
			LastTagTime: testNow.Add(-60 * 24 * time.Hour),
		}
	}
	org := image("800000000008", "example/app", "1", "docker.io/example/app")
	official := image("8b000000000b", "postgres", "16", "docker.io/library/postgres")
	private := image("8c000000000c", "registry.example.com:5000/acme/api", "1.4", "registry.example.com/acme/api")
	nested := image("8d000000000d", "library/distros/ubuntu", "1", "docker.io/library/distros/ubuntu")
	for _, tc := range []struct {
		im      Image
		keep    config.KeepEntry
		offered bool
	}{
		{org, config.KeepEntry{Name: "example/other"}, true},
		{org, config.KeepEntry{Name: "example/app"}, false},
		{org, config.KeepEntry{Name: "example/app:*"}, false},
		{org, config.KeepEntry{Name: "example/*"}, false},
		{org, config.KeepEntry{Name: "docker.io/example/app"}, false},
		{org, config.KeepEntry{Name: "docker.io/example/*"}, false},
		{org, config.KeepEntry{Name: "docker.io/example/app:1"}, false},
		{org, config.KeepEntry{Name: "index.docker.io/example/app"}, false},
		{org, config.KeepEntry{Name: "index.docker.io/example/app:1"}, false},
		{org, config.KeepEntry{Name: "docker.io/example/app:2"}, true},
		{org, config.KeepEntry{Name: "index.docker.io/example/app:2"}, true},
		{org, config.KeepEntry{Name: "ghcr.io/example/app"}, true},
		// docker reads these as docker.io/library/app and
		// docker.io/library/example/app, other images.
		{org, config.KeepEntry{Name: "docker.io/app"}, true},
		{org, config.KeepEntry{Name: "library/example/app"}, true},
		// An image has no compose project.
		{org, config.KeepEntry{Project: "example"}, true},
		{official, config.KeepEntry{Name: "postgres"}, false},
		{official, config.KeepEntry{Name: "postgres:16"}, false},
		{official, config.KeepEntry{Name: "library/postgres"}, false},
		{official, config.KeepEntry{Name: "library/postgres:16"}, false},
		{official, config.KeepEntry{Name: "docker.io/library/postgres"}, false},
		{official, config.KeepEntry{Name: "docker.io/library/postgres:16"}, false},
		{official, config.KeepEntry{Name: "docker.io/library/postgres:15"}, true},
		// docker reads a one-part docker.io name as library/<name>, and
		// index.docker.io as docker.io.
		{official, config.KeepEntry{Name: "docker.io/postgres"}, false},
		{official, config.KeepEntry{Name: "docker.io/postgres:16"}, false},
		{official, config.KeepEntry{Name: "index.docker.io/postgres"}, false},
		{official, config.KeepEntry{Name: "index.docker.io/postgres:16"}, false},
		{official, config.KeepEntry{Name: "index.docker.io/library/postgres"}, false},
		{official, config.KeepEntry{Name: "index.docker.io/library/postgres:16"}, false},
		{official, config.KeepEntry{Name: "index.docker.io/postgres:15"}, true},
		{official, config.KeepEntry{Name: "ghcr.io/postgres"}, true},
		{official, config.KeepEntry{Name: "example/postgres"}, true},
		{private, config.KeepEntry{Name: "registry.example.com:5000/acme/api"}, false},
		{private, config.KeepEntry{Name: "registry.example.com:5000/acme/*:1.4"}, false},
		// The name without the registry's port.
		{private, config.KeepEntry{Name: "registry.example.com/acme/api"}, false},
		{private, config.KeepEntry{Name: "registry.example.com/acme/api:1.4"}, false},
		{private, config.KeepEntry{Name: "registry.example.com/acme/api:1.5"}, true},
		// Not a Docker Hub image: no shorter spelling names it.
		{private, config.KeepEntry{Name: "acme/api"}, true},
		{private, config.KeepEntry{Name: "docker.io/acme/api"}, true},
		{private, config.KeepEntry{Name: "index.docker.io/acme/api"}, true},
		// docker drops library/ from a one-part name only.
		{nested, config.KeepEntry{Name: "docker.io/library/distros/ubuntu"}, false},
		{nested, config.KeepEntry{Name: "distros/ubuntu"}, true},
	} {
		cfg := config.Docker{Keep: []config.KeepEntry{tc.keep}}
		got := classify(&Snapshot{Images: []Image{tc.im}}, cfg, 0, testNow).imagesUnused
		if (len(got) == 1) != tc.offered {
			t.Errorf("%s with keep %+v: offered = %v, want %v", imageName(tc.im), tc.keep, len(got) == 1, tc.offered)
		}
	}
}

// Each of an image's names is held with its own tag.
func TestKeepListPairsEachNameWithItsOwnTag(t *testing.T) {
	im := Image{Repository: "postgres", Tag: "16", Tags: []string{"postgres:16", "example/pg:prod"}}
	for name, want := range map[string]bool{
		"postgres:16":               true,
		"example/pg":                true,
		"example/pg:prod":           true,
		"docker.io/example/pg:prod": true,
		"example/pg:16":             false,
		"postgres:prod":             false,
	} {
		cfg := config.Docker{Keep: []config.KeepEntry{{Name: name}}}
		if got := keepsImage(cfg, im); got != want {
			t.Errorf("keep %q keeps the image = %v, want %v", name, got, want)
		}
	}
	// A name by digest has no tag for an entry to name.
	pinned := Image{Repository: "<none>", Tag: "<none>", Tags: []string{"example/pinned@sha256:" + strings.Repeat("f", 64)}}
	for name, want := range map[string]bool{"example/pinned": true, "example/pinned:*": false} {
		cfg := config.Docker{Keep: []config.KeepEntry{{Name: name}}}
		if got := keepsImage(cfg, pinned); got != want {
			t.Errorf("keep %q keeps the image pulled by digest = %v, want %v", name, got, want)
		}
	}
}

// The keep-list names images, and a dangling image has no name: an
// entry that matches anything must not hide the dangling tier.
func TestKeepListLeavesDanglingImagesOffered(t *testing.T) {
	snap := &Snapshot{Images: []Image{{ID: imageID("300000000003"), Repository: "<none>", Tag: "<none>"}}}
	cfg := config.Docker{Keep: []config.KeepEntry{{Name: "*"}}}
	if got := classify(snap, cfg, 0, testNow).imagesDangling; len(got) != 1 {
		t.Errorf("dangling tier = %v with keep \"*\", want the untagged image", keys(got))
	}
}

// df names the image and inspect lists no name for it: a tag removed
// between the two calls. Nothing says what it is, so neither rule
// takes it.
func TestNamedRowWithoutRepoTagsIsInNeitherTier(t *testing.T) {
	id := imageID("ad000000000d")
	rows := []dfImage{{ID: id, Repository: "example/moved", Tag: "1",
		CreatedAt: "2026-04-02 12:00:00 +0000 UTC", UniqueSize: "30MB", Containers: "0"}}
	inspected, err := parseImageInspect([]byte(`[{"Id":"` + id + `","RepoTags":[],` +
		`"Metadata":{"LastTagTime":"2026-06-01T08:00:00Z"},` +
		`"Identity":{"Pull":[{"Repository":"docker.io/example/moved"}]}}]`))
	if err != nil {
		t.Fatal(err)
	}
	tr := classify(&Snapshot{Images: joinImages(rows, nil, inspected)}, config.Docker{}, 0, testNow)
	if len(tr.imagesDangling) != 0 || len(tr.imagesUnused) != 0 {
		t.Errorf("dangling=%v unused=%v, want neither: df names it and inspect lists no tag",
			keys(tr.imagesDangling), keys(tr.imagesUnused))
	}
}

// Thirty days is measured from the last tag time to the scan.
func TestUnusedImageAgeBoundary(t *testing.T) {
	image := func(tagged time.Time) *Snapshot {
		return &Snapshot{Images: []Image{{
			ID:          imageID("800000000008"),
			Repository:  "example/app",
			Tag:         "1",
			CreatedAt:   testNow.Add(-365 * 24 * time.Hour),
			Tags:        []string{"example/app:1"},
			PulledFrom:  []string{"docker.io/example/app"},
			LastTagTime: tagged,
		}}}
	}
	for _, tc := range []struct {
		name    string
		tagged  time.Time
		offered bool
	}{
		{"29 days", testNow.Add(-29 * 24 * time.Hour), false},
		{"one second short of 30 days", testNow.Add(-staleAge + time.Second), false},
		{"30 days", testNow.Add(-staleAge), true},
		{"31 days", testNow.Add(-31 * 24 * time.Hour), true},
	} {
		got := classify(image(tc.tagged), config.Docker{}, 0, testNow).imagesUnused
		if (len(got) == 1) != tc.offered {
			t.Errorf("tagged %s ago: offered = %v, want %v", tc.name, len(got) == 1, tc.offered)
		}
	}
}

func cannedExec(t *testing.T, inspect func() ([]byte, error)) Exec {
	t.Helper()
	return func(_ context.Context, args ...string) ([]byte, bool, error) {
		switch strings.Join(args[:2], " ") {
		case "system df":
			return []byte(cannedDF()), true, nil
		case "container inspect":
			return []byte(cannedContainers()), true, nil
		case "image inspect":
			out, err := inspect()
			return out, true, err
		}
		t.Fatalf("unexpected docker call: %v", args)
		return nil, false, nil
	}
}

// The query asks for every image by ID with no --format, and the
// answer reaches the rule through the provider.
func TestImagesUnusedQuery(t *testing.T) {
	var asked []string
	run := func(ctx context.Context, args ...string) ([]byte, bool, error) {
		if args[0] == "image" {
			asked = args[2:]
		}
		return cannedExec(t, func() ([]byte, error) { return []byte(cannedImageInspect()), nil })(ctx, args...)
	}
	items, err := testProvider(t, run).ImagesUnused(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := keys(items)
	sort.Strings(got)
	want := []string{"3a000000003a", "600000000006", "700000000007", "7a000000007a", "800000000008"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("ImagesUnused = %v, want %v", got, want)
	}
	if len(asked) != 24 || asked[0] != imageID("100000000001") {
		t.Errorf("image inspect was asked for %v, want the 24 image IDs and no flags", asked)
	}
}

// An image removed between df and inspect makes the CLI exit non-zero
// with the rest printed: the rest still classify.
func TestImageInspectPartialAnswerIsUsed(t *testing.T) {
	run := cannedExec(t, func() ([]byte, error) {
		return []byte(cannedImageInspect()), errors.New("exit status 1")
	})
	items, err := testProvider(t, run).ImagesUnused(context.Background())
	if err != nil || len(items) != 5 {
		t.Fatalf("items=%v err=%v, want the 5 offered images and no error", keys(items), err)
	}
}

// A daemon that answered df and then nothing must not read as "no
// image has tags or an origin".
func TestImageInspectWithNoAnswerIsAnError(t *testing.T) {
	for name, out := range map[string]string{"empty array": "[]\n", "no output": ""} {
		t.Run(name, func(t *testing.T) {
			run := cannedExec(t, func() ([]byte, error) {
				return []byte(out), errors.New("Cannot connect to the Docker daemon")
			})
			p := testProvider(t, run)
			if _, err := p.ImagesUnused(context.Background()); err == nil {
				t.Fatal("a failed image inspect with nothing returned must be an error")
			}
			if _, err := p.ImagesDangling(context.Background()); err == nil {
				t.Fatal("the dangling rule must report the same error")
			}
		})
	}
}

func TestImagesUnusedDoesNotApplyWithoutDocker(t *testing.T) {
	for name, run := range map[string]Exec{
		"binary missing": func(context.Context, ...string) ([]byte, bool, error) {
			return nil, false, nil
		},
		"daemon stopped": func(context.Context, ...string) ([]byte, bool, error) {
			return nil, true, errors.New("Cannot connect to the Docker daemon")
		},
	} {
		t.Run(name, func(t *testing.T) {
			items, err := testProvider(t, run).ImagesUnused(context.Background())
			if err != nil || items != nil {
				t.Fatalf("items=%v err=%v, want no items and no error", items, err)
			}
		})
	}
}

func TestImagesUnusedReportsTheDeadline(t *testing.T) {
	wedged := func(ctx context.Context, _ ...string) ([]byte, bool, error) {
		<-ctx.Done()
		return nil, true, errors.New("signal: killed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := testProvider(t, wedged).ImagesUnused(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wedged daemon = %v, want the deadline", err)
	}
}
