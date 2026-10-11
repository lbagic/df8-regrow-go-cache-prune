package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Exec runs the docker CLI and returns stdout. found is false only
// when the binary is not on PATH. Stdout is returned even when the
// command exits non-zero: batch `inspect` prints every object it did
// find and errors only for the missing ones.
type Exec func(ctx context.Context, args ...string) (out []byte, found bool, err error)

// realExec is the production Exec.
func realExec(ctx context.Context, args ...string) ([]byte, bool, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, false, nil
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	// A killed CLI may leave a child holding stdout; stop waiting.
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	return out, true, err
}

// Volume is one docker volume with everything the classifier needs.
type Volume struct {
	Name      string
	CreatedAt time.Time
	Labels    map[string]string
	Bytes     int64
	// UsedBy lists the names of containers (running or stopped) whose
	// mounts reference the volume. Non-empty means protected.
	UsedBy []string
	// LastUsed is the derived timestamp: the newest StartedAt or
	// FinishedAt across referencing containers, merged with the usage
	// ledger. Zero when nothing was ever observed.
	LastUsed time.Time
}

// Anonymous reports whether docker created the volume implicitly (the
// label is authoritative; anonymous volumes also get 64-hex names).
func (v Volume) Anonymous() bool {
	_, ok := v.Labels["com.docker.volume.anonymous"]
	return ok
}

// Project is the compose project the volume belongs to, if any.
func (v Volume) Project() string {
	return v.Labels["com.docker.compose.project"]
}

// Container is one container, running or stopped.
type Container struct {
	ID    string
	Name  string
	Image string
	// ImageID is the ID of the image the container runs.
	ImageID    string
	Status     string // created, running, paused, restarting, removing, exited, dead
	Created    time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	// VolumeNames are the names of volume-type mounts.
	VolumeNames []string
	// ImageMounts are the image-type mounts' sources as the container
	// was created with them: a name or an ID, never resolved.
	ImageMounts []string
	Labels      map[string]string
	// Bytes is the writable layer (what `docker rm` frees).
	Bytes int64
}

// Stopped reports whether the container is not alive in any form.
func (c Container) Stopped() bool {
	switch c.Status {
	case "created", "exited", "dead":
		return true
	}
	return false
}

// LastUsed is the container's own recency: the newest of its
// lifecycle timestamps.
func (c Container) LastUsed() time.Time {
	t := c.Created
	if c.StartedAt.After(t) {
		t = c.StartedAt
	}
	if c.FinishedAt.After(t) {
		t = c.FinishedAt
	}
	return t
}

// Project is the compose project, if any.
func (c Container) Project() string {
	return c.Labels["com.docker.compose.project"]
}

// Image is one image: its `system df -v` row joined with the
// containers that run it and with `image inspect`.
type Image struct {
	ID         string
	Repository string
	Tag        string
	CreatedAt  time.Time
	// Bytes is the unique size — what deleting this image alone frees.
	// Layers shared with another image are in neither image's figure.
	Bytes int64
	// Containers is the number of containers, running or stopped,
	// that run or mount the image; -1 when docker could not say.
	Containers int
	// Tags are inspect's RepoTags. The containerd image store lists a
	// digest reference (repo@sha256:…) here too.
	Tags []string
	// PulledFrom lists the registry repositories docker recorded the
	// image's content as pulled from or pushed to (inspect
	// .Identity.Pull, Engine API 1.53+), spelled host/path with the
	// registry's port dropped. Empty for an image only built or loaded
	// here, and on older engines. RepoDigests is no substitute: the
	// containerd image store gives a locally built image one too.
	PulledFrom []string
	// Built is true when docker recorded a build of the image here
	// (inspect .Identity.Build).
	Built bool
	// LastTagTime is the local pull/tag time; zero when unknown.
	LastTagTime time.Time
}

// Dangling reports an image df prints no name for and inspect lists
// nothing in RepoTags for. The df row alone cannot say: it prints
// <none> for an image known only by a digest reference too. The
// containerd image store lists that reference in RepoTags; the classic
// one does not, and there such an image is dangling.
func (i Image) Dangling() bool {
	return (i.Repository == "<none>" || i.Repository == "") && len(i.Tags) == 0
}

// InUse reports whether a container runs the image. An unknown count
// is in use: only a known zero may be offered for removal.
func (i Image) InUse() bool { return i.Containers != 0 }

// BuildCache is one buildkit cache record — the only docker object
// with a native last-used.
type BuildCache struct {
	ID         string
	Bytes      int64
	InUse      bool
	Shared     bool
	LastUsedAt time.Time
}

// Snapshot is one scan-time view of the daemon.
type Snapshot struct {
	Volumes    []Volume
	Containers []Container
	Images     []Image
	BuildCache []BuildCache
}

// dfJSON mirrors `docker system df -v --format {{json .}}`. Numbers
// and booleans arrive as strings; sizes are human-formatted (SI).
type dfJSON struct {
	Images     []dfImage `json:"Images"`
	Containers []struct {
		ID   string `json:"ID"`
		Size string `json:"Size"`
	} `json:"Containers"`
	Volumes []struct {
		Name  string `json:"Name"`
		Links string `json:"Links"`
		Size  string `json:"Size"`
	} `json:"Volumes"`
	BuildCache []struct {
		ID         string `json:"ID"`
		Size       string `json:"Size"`
		InUse      string `json:"InUse"`
		Shared     string `json:"Shared"`
		LastUsedAt string `json:"LastUsedAt"`
	} `json:"BuildCache"`
}

type dfImage struct {
	ID         string `json:"ID"`
	Repository string `json:"Repository"`
	Tag        string `json:"Tag"`
	CreatedAt  string `json:"CreatedAt"`
	UniqueSize string `json:"UniqueSize"`
	Containers string `json:"Containers"`
}

// containerInspectFormat trims `container inspect` to what the join
// needs — never the full config, which drags env vars (secrets) into
// memory and test fixtures.
const containerInspectFormat = `{"ID":{{json .Id}},"Name":{{json .Name}},"Created":{{json .Created}},"Status":{{json .State.Status}},"StartedAt":{{json .State.StartedAt}},"FinishedAt":{{json .State.FinishedAt}},"Mounts":{{json .Mounts}},"Labels":{{json .Config.Labels}},"Image":{{json .Config.Image}},"ImageID":{{json .Image}}}`

type containerJSON struct {
	ID         string `json:"ID"`
	Name       string `json:"Name"`
	Created    string `json:"Created"`
	Status     string `json:"Status"`
	StartedAt  string `json:"StartedAt"`
	FinishedAt string `json:"FinishedAt"`
	Mounts     []struct {
		Type string `json:"Type"`
		Name string `json:"Name"`
	} `json:"Mounts"`
	Labels  map[string]string `json:"Labels"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
}

type volumeJSON struct {
	Name      string            `json:"Name"`
	CreatedAt string            `json:"CreatedAt"`
	Labels    map[string]string `json:"Labels"`
}

// imageInspectJSON is what the image rules read of `docker image
// inspect`. The call takes no --format: a template that names
// .Identity is an error on a docker older than the field ("map has no
// entry for key"), and a failed inspect fails every docker rule.
type imageInspectJSON struct {
	ID       string   `json:"Id"`
	RepoTags []string `json:"RepoTags"`
	Metadata struct {
		LastTagTime string `json:"LastTagTime"`
	} `json:"Metadata"`
	Identity *struct {
		Pull []struct {
			Repository string `json:"Repository"`
		} `json:"Pull"`
		Build []struct {
			Ref string `json:"Ref"`
		} `json:"Build"`
	} `json:"Identity"`
}

// loadSnapshot enumerates the daemon. (nil, nil) means docker is not
// available right now — binary missing or daemon down — so the rules
// simply do not apply. Any failure after the probe succeeds is a real
// error: a half-visible daemon must not classify volumes as dangling.
func loadSnapshot(ctx context.Context, run Exec) (*Snapshot, error) {
	out, found, err := run(ctx, "system", "df", "-v", "--format", "{{json .}}")
	if ctxErr := ctx.Err(); err != nil && ctxErr != nil {
		// The query's deadline, not a stopped daemon: every docker
		// rule must report it rather than "not found".
		return nil, fmt.Errorf("docker system df: %w", ctxErr)
	}
	if !found || err != nil {
		return nil, nil
	}
	var df dfJSON
	if err := json.Unmarshal(out, &df); err != nil {
		return nil, fmt.Errorf("docker system df: %w", err)
	}

	snap := &Snapshot{}

	containerBytes := map[string]int64{}
	ids := make([]string, 0, len(df.Containers))
	for _, c := range df.Containers {
		ids = append(ids, c.ID)
		containerBytes[c.ID], _ = parseSize(c.Size)
	}
	if len(ids) > 0 {
		args := append([]string{"container", "inspect", "--format", containerInspectFormat}, ids...)
		out, _, err := run(ctx, args...)
		containers, perr := parseContainerLines(out)
		// A vanished container is fine (inspect prints the rest); a
		// call that produced nothing while containers exist is not.
		if perr != nil || (err != nil && len(containers) == 0) {
			return nil, fmt.Errorf("docker container inspect: %w", firstErr(err, perr))
		}
		for i := range containers {
			containers[i].Bytes = containerBytes[containers[i].ID]
		}
		snap.Containers = containers
	}

	volumeBytes := map[string]int64{}
	volumeLinks := map[string]int{}
	names := make([]string, 0, len(df.Volumes))
	for _, v := range df.Volumes {
		names = append(names, v.Name)
		volumeBytes[v.Name], _ = parseSize(v.Size)
		volumeLinks[v.Name], _ = strconv.Atoi(v.Links)
	}
	if len(names) > 0 {
		args := append([]string{"volume", "inspect"}, names...)
		out, _, err := run(ctx, args...)
		var vols []volumeJSON
		if perr := json.Unmarshal(out, &vols); perr != nil {
			return nil, fmt.Errorf("docker volume inspect: %w", firstErr(err, perr))
		}
		for _, v := range vols {
			snap.Volumes = append(snap.Volumes, Volume{
				Name:      v.Name,
				CreatedAt: parseDockerTime(v.CreatedAt),
				Labels:    v.Labels,
				Bytes:     volumeBytes[v.Name],
			})
		}
	}

	if len(df.Images) > 0 {
		args := []string{"image", "inspect"}
		for _, im := range df.Images {
			args = append(args, im.ID)
		}
		out, _, err := run(ctx, args...)
		inspected, perr := parseImageInspect(out)
		// An image removed since df is fine (inspect prints the rest);
		// a call that produced nothing while images exist is not.
		if perr != nil || (err != nil && len(inspected) == 0) {
			return nil, fmt.Errorf("docker image inspect: %w", firstErr(err, perr))
		}
		snap.Images = joinImages(df.Images, snap.Containers, inspected)
	}

	for _, bc := range df.BuildCache {
		bytes, _ := parseSize(bc.Size)
		inUse, _ := strconv.ParseBool(bc.InUse)
		shared, _ := strconv.ParseBool(bc.Shared)
		snap.BuildCache = append(snap.BuildCache, BuildCache{
			ID:         bc.ID,
			Bytes:      bytes,
			InUse:      inUse,
			Shared:     shared,
			LastUsedAt: parseDockerTime(bc.LastUsedAt),
		})
	}

	joinVolumeUsage(snap, volumeLinks)
	return snap, nil
}

// parseImageInspect reads `docker image inspect` output, keyed by
// image ID. Empty output is no images, not an error: the CLI prints
// nothing useful when every image it was asked for is gone.
func parseImageInspect(out []byte) (map[string]imageInspectJSON, error) {
	if strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}
	var list []imageInspectJSON
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, err
	}
	inspected := make(map[string]imageInspectJSON, len(list))
	for _, m := range list {
		inspected[m.ID] = m
	}
	return inspected, nil
}

// joinImages builds each image from docker's three views of it: the
// `system df -v` row (unique size, container count), the containers
// (which image each one runs or mounts) and inspect (tags, origin,
// last tag time). The container join backs up df's count, and an
// unreadable count stays unknown: reading an image a container uses as
// unused is the one direction this must never fail in. df counts no
// image mounts and an unforced removal does not check them, so a mount
// by full ID counts as use and a mount by any other reference makes
// every image's use unknown: a tag can have moved since the mount
// resolved it. An image inspect did not return gets no row.
func joinImages(rows []dfImage, containers []Container, inspected map[string]imageInspectJSON) []Image {
	runs := map[string]int{}
	unresolvedMount := false
	for _, c := range containers {
		if c.ImageID != "" {
			runs[c.ImageID]++
		}
		for _, ref := range c.ImageMounts {
			if id, ok := fullImageID(ref); ok {
				runs[id]++
			} else {
				unresolvedMount = true
			}
		}
	}
	images := make([]Image, 0, len(rows))
	for _, row := range rows {
		m, ok := inspected[row.ID]
		if !ok {
			continue
		}
		count, err := strconv.Atoi(row.Containers)
		if err != nil || count < 0 {
			count = -1
		}
		if joined := runs[row.ID]; joined > 0 && joined > count {
			count = joined
		}
		if unresolvedMount {
			count = -1
		}
		bytes, _ := parseSize(row.UniqueSize)
		im := Image{
			ID:         row.ID,
			Repository: row.Repository,
			Tag:        row.Tag,
			CreatedAt:  parseDockerTime(row.CreatedAt),
			Bytes:      bytes,
			Containers: count,
			Tags:       m.RepoTags,
		}
		// The containerd image store reports the Unix epoch when it has
		// no tag time.
		if t := parseDockerTime(m.Metadata.LastTagTime); t.After(time.Unix(0, 0)) {
			im.LastTagTime = t
		}
		if m.Identity != nil {
			for _, p := range m.Identity.Pull {
				if p.Repository != "" {
					im.PulledFrom = append(im.PulledFrom, p.Repository)
				}
			}
			im.Built = len(m.Identity.Build) > 0
		}
		images = append(images, im)
	}
	return images
}

// fullImageID returns ref as an image ID ("sha256:" and 64 hex digits)
// when ref spells one in full, with or without the "sha256:".
func fullImageID(ref string) (string, bool) {
	hex := strings.TrimPrefix(ref, "sha256:")
	if len(hex) != 64 || strings.Trim(hex, "0123456789abcdef") != "" {
		return "", false
	}
	return "sha256:" + hex, true
}

// repository splits an image reference (name, name:tag or
// name@digest) into its registry host, without a port, and its path,
// normalised as docker normalises names: no host means docker.io, and
// a one-part docker.io path lives under library/. Inspect's
// Identity.Pull entries are already normalised and portless and come
// out unchanged.
func repository(ref string) (host, path string) {
	name, _ := splitTag(ref)
	host, path, ok := strings.Cut(name, "/")
	if !ok || (!strings.ContainsAny(host, ".:") && host != "localhost" && strings.ToLower(host) == host) {
		host, path = "docker.io", name
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "index.docker.io" {
		host = "docker.io"
	}
	if host == "docker.io" && !strings.Contains(path, "/") {
		path = "library/" + path
	}
	return host, path
}

// splitTag cuts an image reference into its name and its tag. A
// reference by digest alone has no tag.
func splitTag(ref string) (name, tag string) {
	name, _, _ = strings.Cut(ref, "@")
	if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
		return name[:i], name[i+1:]
	}
	return name, ""
}

// spellings lists the names a keep entry may spell the repository by.
// For docker.io that is every name docker reads as it: docker.io is
// the default registry, index.docker.io its older name, and a one-part
// path lives under library/. For another registry it is host/path as
// repository returns them, without the port.
func spellings(host, path string) []string {
	if host != "docker.io" {
		return []string{host + "/" + path}
	}
	paths := []string{path}
	if name, ok := strings.CutPrefix(path, "library/"); ok && !strings.Contains(name, "/") {
		paths = append(paths, name)
	}
	names := make([]string, 0, 3*len(paths))
	for _, p := range paths {
		names = append(names, p, "docker.io/"+p, "index.docker.io/"+p)
	}
	return names
}

// joinVolumeUsage derives each volume's references and last-used from
// the containers that mount it (research §2). The df link count backs
// up the join: if inspect missed a referencing container, Links>0
// still marks the volume as used — misclassifying protected data as
// dangling is the one direction this must never fail in.
func joinVolumeUsage(snap *Snapshot, links map[string]int) {
	byName := map[string]*Volume{}
	for i := range snap.Volumes {
		byName[snap.Volumes[i].Name] = &snap.Volumes[i]
	}
	for _, c := range snap.Containers {
		for _, name := range c.VolumeNames {
			v, ok := byName[name]
			if !ok {
				continue
			}
			v.UsedBy = append(v.UsedBy, c.Name)
			if t := c.LastUsed(); t.After(v.LastUsed) {
				v.LastUsed = t
			}
		}
	}
	for i := range snap.Volumes {
		v := &snap.Volumes[i]
		sort.Strings(v.UsedBy)
		if len(v.UsedBy) == 0 && links[v.Name] > 0 {
			v.UsedBy = []string{"(unknown container)"}
		}
	}
}

func parseContainerLines(out []byte) ([]Container, error) {
	var containers []Container
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var c containerJSON
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, err
		}
		var volNames, imageMounts []string
		for _, m := range c.Mounts {
			switch {
			case m.Type == "volume" && m.Name != "":
				volNames = append(volNames, m.Name)
			case m.Type == "image":
				imageMounts = append(imageMounts, m.Name)
			}
		}
		containers = append(containers, Container{
			ID:          c.ID,
			Name:        strings.TrimPrefix(c.Name, "/"),
			Image:       c.Image,
			ImageID:     c.ImageID,
			Status:      c.Status,
			Created:     parseDockerTime(c.Created),
			StartedAt:   parseDockerTime(c.StartedAt),
			FinishedAt:  parseDockerTime(c.FinishedAt),
			VolumeNames: volNames,
			ImageMounts: imageMounts,
			Labels:      c.Labels,
		})
	}
	return containers, nil
}

// parseDockerTime handles the two formats the CLI emits: RFC3339
// (inspect) and Go's time.String() (`system df -v`). Docker's zero
// timestamps ("0001-01-01T00:00:00Z") parse to time.Time zero, which
// is exactly what LastUsed-zero means. Unparseable input is treated
// as unknown, never as an error — a timestamp is a recency signal,
// not a contract.
func parseDockerTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05 -0700 MST"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// parseSize parses docker's human sizes ("1.5GB (50%)", "0B"). Docker
// uses SI units: kB = 1000 bytes.
func parseSize(s string) (int64, bool) {
	if before, _, found := strings.Cut(s, " "); found {
		s = before
	}
	i := strings.IndexFunc(s, func(r rune) bool {
		return r != '.' && (r < '0' || r > '9')
	})
	if i <= 0 {
		return 0, false
	}
	val, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, false
	}
	mult, ok := map[string]float64{
		"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
	}[s[i:]]
	if !ok {
		return 0, false
	}
	return int64(val * mult), true
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
