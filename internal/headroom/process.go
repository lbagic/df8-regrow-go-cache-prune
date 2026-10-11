package headroom

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Process struct {
	PID  int    `json:"pid"`
	PPID int    `json:"ppid,omitempty"`
	Name string `json:"name"`
	RSS  int64  `json:"rss"` // resident bytes
}

func Processes(ctx context.Context) ([]Process, error) {
	out, err := probeOutput(ctx, probeWaitDelay, "ps", "-axo", "pid=,ppid=,rss=,comm=")
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return parsePS(out), nil
}

// parsePS reads `ps -axo pid=,ppid=,rss=,comm=`: rss is in KiB, and
// comm is the executable path, which may contain spaces.
func parsePS(out []byte) []Process {
	var ps []Process
	for _, line := range strings.Split(string(out), "\n") {
		var nums [3]int64
		rest := strings.TrimSpace(line)
		ok := true
		for i := range nums {
			var field string
			field, rest, _ = strings.Cut(rest, " ")
			rest = strings.TrimSpace(rest)
			n, err := strconv.ParseInt(field, 10, 64)
			if err != nil {
				ok = false
				break
			}
			nums[i] = n
		}
		if !ok || rest == "" {
			continue
		}
		ps = append(ps, Process{PID: int(nums[0]), PPID: int(nums[1]), Name: filepath.Base(rest), RSS: nums[2] * 1024})
	}
	return ps
}

// Top is the process with the most resident memory.
func Top(ps []Process) (Process, bool) {
	var top Process
	for _, p := range ps {
		if p.RSS > top.RSS {
			top = p
		}
	}
	return top, top.RSS > 0
}

// Running returns which of the names have a process, sorted. The
// process self and its ancestors do not count: under `go run`, the go
// command that started regrow is not a build.
func Running(ps []Process, names []string, self int) []string {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	parent := make(map[int]int, len(ps))
	for _, p := range ps {
		parent[p.PID] = p.PPID
	}
	mine := map[int]bool{}
	for pid := self; pid > 1 && !mine[pid]; pid = parent[pid] {
		mine[pid] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range ps {
		if want[p.Name] && !seen[p.Name] && !mine[p.PID] {
			seen[p.Name] = true
			out = append(out, p.Name)
		}
	}
	sort.Strings(out)
	return out
}
