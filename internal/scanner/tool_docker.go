package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lbagic/regrow/internal/docker"
	"github.com/lbagic/regrow/internal/engine"
)

// The docker rules share one provider (internal/docker): the daemon
// is enumerated and classified once per scan, and each rule's query
// reads its tier. Docker installed but daemon down is the everyday
// laptop case — the provider reports it as "does not apply", never as
// a scan error.
func dockerQueries() map[string]ToolQuery {
	return dockerQueriesOf(&docker.Provider{})
}

func dockerQueriesOf(p *docker.Provider) map[string]ToolQuery {
	return map[string]ToolQuery{
		"docker-volumes-named":      p.VolumesNamed,
		"docker-volumes-anon":       p.VolumesAnon,
		"docker-volumes-kept":       p.VolumesKept,
		"docker-containers-stopped": p.ContainersStopped,
		"docker-images-dangling":    p.ImagesDangling,
		"docker-images-unused":      p.ImagesUnused,
		"docker-build-cache":        p.BuildCache,
		"docker-vm-disk":            queryDockerVMDisk,
	}
}

// queryDockerVMDisk sizes the Docker Desktop VM disk real-vs-logical
// (Prompt H phantom space): the file is sparse, so allocated blocks
// (what deletion would reclaim — but we never delete it) sit far below
// the apparent size Finder-style tools report. A file stat, not a
// daemon call: the phantom explainer must work with Docker stopped.
func queryDockerVMDisk(ctx context.Context) ([]engine.Item, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}
	for _, name := range []string{"Docker.raw", "Docker.qcow2"} {
		p := filepath.Join(home, "Library/Containers/com.docker.docker/Data/vms/0/data", name)
		fi, err := defaultWalker.stat(ctx, p)
		if errors.Is(err, errBlocked) {
			return []engine.Item{{Label: name, Path: p, Partial: true}}, nil
		}
		if err != nil {
			continue
		}
		real := physicalSize(fi)
		return []engine.Item{{
			Label: fmt.Sprintf("%s — %s real of %s sparse", name, engine.HumanBytes(real), engine.HumanBytes(fi.Size())),
			Path:  p,
			Bytes: real,
		}}, nil
	}
	return nil, nil
}
