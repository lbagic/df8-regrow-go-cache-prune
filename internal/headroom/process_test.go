package headroom

import (
	"reflect"
	"testing"
)

const psOutput = `    1     0  21264 /sbin/launchd
  412     1 8388608 /Applications/Virtual Box.app/Contents/MacOS/Virtual Machine Service
 9001  9002 524288 /usr/local/go/pkg/tool/darwin_arm64/compile
 9002   700  10240 /usr/local/go/bin/go
 9003     1   4096 gopls
garbage line
 9004 1
`

func TestParsePS(t *testing.T) {
	ps := parsePS([]byte(psOutput))
	want := []Process{
		{PID: 1, PPID: 0, Name: "launchd", RSS: 21264 << 10},
		{PID: 412, PPID: 1, Name: "Virtual Machine Service", RSS: 8 * GiB},
		{PID: 9001, PPID: 9002, Name: "compile", RSS: 524288 << 10},
		{PID: 9002, PPID: 700, Name: "go", RSS: 10240 << 10},
		{PID: 9003, PPID: 1, Name: "gopls", RSS: 4096 << 10},
	}
	if !reflect.DeepEqual(ps, want) {
		t.Fatalf("parsePS = %+v\nwant %+v", ps, want)
	}
	top, ok := Top(ps)
	if !ok || top.PID != 412 {
		t.Errorf("Top = %+v, %v; want pid 412", top, ok)
	}
	if _, ok := Top(nil); ok {
		t.Error("Top of no processes must report none")
	}
}

func TestRunning(t *testing.T) {
	// A shell (100) ran `go run ./cmd/regrow` (go, 200), which runs
	// regrow (300). A separate build runs go (400) → compile (401).
	ps := []Process{
		{PID: 100, PPID: 1, Name: "zsh"},
		{PID: 200, PPID: 100, Name: "go"},
		{PID: 300, PPID: 200, Name: "regrow"},
		{PID: 400, PPID: 1, Name: "go"},
		{PID: 401, PPID: 400, Name: "compile"},
		{PID: 500, PPID: 1, Name: "gopls"},
	}
	names := []string{"go", "compile", "link"}
	tests := []struct {
		name string
		ps   []Process
		self int
		want []string
	}{
		{"a separate build counts", ps, 300, []string{"compile", "go"}},
		{"the go run that started regrow does not", ps[:3], 300, nil},
		{"without self, every go counts", ps[:3], 0, []string{"go"}},
		{"gopls is not go: names match whole", ps[4:], 300, []string{"compile"}},
	}
	for _, tt := range tests {
		if got := Running(tt.ps, names, tt.self); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: Running = %v, want %v", tt.name, got, tt.want)
		}
	}
	// A cycle in a stale snapshot must not hang the walk.
	loop := []Process{{PID: 10, PPID: 11, Name: "go"}, {PID: 11, PPID: 10, Name: "go"}}
	if got := Running(loop, names, 10); got != nil {
		t.Errorf("Running on a cycle = %v, want none", got)
	}
}

func TestParsePurgeable(t *testing.T) {
	got, err := parsePurgeable([]byte(`{"important":60000000000,"available":35000000000}`))
	if err != nil || got != 25000000000 {
		t.Errorf("parsePurgeable = %d, %v; want 25000000000", got, err)
	}
	// Finder's number can lag under df's for a moment; never negative.
	if got, err := parsePurgeable([]byte(`{"important":10,"available":20}`)); err != nil || got != 0 {
		t.Errorf("parsePurgeable = %d, %v; want 0", got, err)
	}
	if _, err := parsePurgeable([]byte(`execution error`)); err == nil {
		t.Error("unparseable probe output must be an error")
	}
}
