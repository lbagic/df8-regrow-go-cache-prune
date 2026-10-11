package protocol

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/oplog"
)

func sh(script string) []string { return []string{"/bin/sh", "-c", script} }

func TestRunNativeFailureCarriesTheOutputTail(t *testing.T) {
	err := RunNative(context.Background(), sh(`echo to-stdout; echo to-stderr >&2; exit 3`))
	if err == nil {
		t.Fatal("a failing command returned no error")
	}
	for _, want := range []string{"exit status 3", "to-stdout", "to-stderr"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

func TestRunNativeSuccessIsSilent(t *testing.T) {
	if err := RunNative(context.Background(), sh(`echo chatter; echo more >&2`)); err != nil {
		t.Fatalf("a successful command returned %v", err)
	}
}

func TestRunNativeStdinIsEmpty(t *testing.T) {
	// cat copies stdin to stdout: nothing may arrive, and it must end.
	err := RunNative(context.Background(), sh(`cat; echo "after-cat"; exit 1`))
	if err == nil || !strings.HasSuffix(err.Error(), "exit status 1: after-cat") {
		t.Fatalf("err = %v, want only the script's own line after an empty stdin", err)
	}
}

func TestRunNativeKeepsOnlyTheTail(t *testing.T) {
	err := RunNative(context.Background(), sh(`i=0; while [ $i -lt 2000 ]; do echo "line-$i"; i=$((i+1)); done; exit 1`))
	if err == nil {
		t.Fatal("a failing command returned no error")
	}
	msg := err.Error()
	if len(msg) > nativeTailBytes+64 {
		t.Fatalf("error is %d bytes, want the output bounded near %d", len(msg), nativeTailBytes)
	}
	// The kept text is the end of the output and starts at a line
	// boundary.
	_, kept, _ := strings.Cut(msg, "exit status 1: ")
	if !strings.HasSuffix(kept, "line-1999") || !strings.HasPrefix(kept, "line-") || strings.Contains(kept, "line-0\n") {
		t.Fatalf("want whole trailing lines, the first ones dropped, got %.80q…", kept)
	}
}

func TestRunNativeKeepsTheTailOfOneLongLine(t *testing.T) {
	// One line longer than the tail, newline at its end: there is no
	// later line to start at, so the kept end of this one is the text.
	err := RunNative(context.Background(), sh(`printf '%05000d-the-reason\n' 0; exit 1`))
	if err == nil || !strings.HasSuffix(err.Error(), "0000-the-reason") {
		t.Fatalf("err = %.60q…, want the end of the long line", err)
	}
}

func TestRunNativeToSendsStdoutToTheWriter(t *testing.T) {
	var data strings.Builder
	err := RunNativeTo(context.Background(), sh(`printf payload; echo why-it-failed >&2; exit 2`), &data)
	if data.String() != "payload" {
		t.Errorf("writer got %q, want the command's stdout", data.String())
	}
	if err == nil || !strings.HasSuffix(err.Error(), "exit status 2: why-it-failed") {
		t.Fatalf("err = %v, want stderr's tail and nothing of stdout", err)
	}
}

func TestRunNativeRefusesSudo(t *testing.T) {
	err := RunNative(context.Background(), []string{"sudo", "/bin/sh", "-c", "exit 0"})
	if err == nil || !strings.Contains(err.Error(), "administrator rights") {
		t.Fatalf("err = %v, want sudo refused before it runs", err)
	}
	if err := RunNative(context.Background(), nil); err == nil {
		t.Fatal("an empty command returned no error")
	}
}

// The engine's stdin and stdout carry the protocol. A steward command
// that inherited them would write into the event stream and eat the
// next request, so this test stands pipes in for the process's own
// stdio and checks the command touched neither. The command is a
// shell line that deletes nothing.
func TestNativeCommandThatWritesStdoutAndReadsStdin(t *testing.T) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	const nextRequest = "{\"type\":\"scan\",\"id\":\"next\"}\n"
	if _, err := stdinW.WriteString(nextRequest); err != nil {
		t.Fatal(err)
	}
	realIn, realOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = stdinR, stdoutW
	t.Cleanup(func() {
		os.Stdin, os.Stdout = realIn, realOut
		for _, f := range []*os.File{stdinR, stdinW, stdoutR, stdoutW} {
			_ = f.Close()
		}
	})

	m := newMachine(t)
	chatty := engine.Rule{
		ID: "fixture-chatty", Risk: engine.RiskCaution,
		NativeCommand: engine.Argv{"/bin/sh", "-c", `echo to-stdout; echo to-stderr >&2; IFS= read -r line; echo "stdin:[$line]"; exit 3`},
	}
	srv := &Server{
		Host:    m.host(),
		Catalog: []engine.Rule{chatty},
		Scan:    scanOf([]engine.Finding{{Rule: chatty, Items: []engine.Item{{Path: m.cache, Bytes: 100}}}}),
		NewExecutor: func(string) (*executor.Executor, func(), error) {
			log, err := oplog.Open(m.logPath)
			if err != nil {
				return nil, nil, err
			}
			// RunNative left nil: the server must supply its own.
			return &executor.Executor{Log: log}, func() { _ = log.Close() }, nil
		},
	}
	p := start(t, srv)
	p.expect("hello")
	planned := p.planned("p", p.scanned("s", 1), `["fixture-chatty"]`)
	if got := actionRules(planned.Plan); got != "fixture-chatty" || planned.Plan.Actions[0].Kind != engine.ActionNative {
		t.Fatalf("planned %+v", planned.Plan.Actions)
	}

	p.send(`{"type":"execute","id":"x","plan_id":"` + planned.PlanID + `"}`)
	run := p.expect("journal/x", "journal/x", "done/x")
	failed := run[1].Entry
	if failed.Event != oplog.EventFail {
		t.Fatalf("journal = %s, want the failed command recorded", run[1].raw)
	}
	for _, want := range []string{"exit status 3", "to-stdout", "to-stderr", "stdin:[]"} {
		if !strings.Contains(failed.Error, want) {
			t.Errorf("journaled error %q is missing %q", failed.Error, want)
		}
	}
	if entries := m.journal(); len(entries) != 2 || entries[1].Error != failed.Error {
		t.Errorf("the oplog must carry the same error tail, got %+v", entries)
	}
	if run[2].Result.Failed != 1 {
		t.Errorf("result = %s", run[2].raw)
	}
	if rest, err := p.closeAndWait(); err != nil || len(rest) != 0 {
		t.Fatalf("after EOF: err %v, stray events %v", err, rest)
	}

	// Nothing reached the process's stdout, and the request waiting on
	// its stdin is still there for the engine to read.
	_ = stdoutW.Close()
	if leaked, _ := io.ReadAll(stdoutR); len(leaked) != 0 {
		t.Errorf("the command wrote %q to the engine's stdout", leaked)
	}
	_ = stdinW.Close()
	if left, _ := io.ReadAll(stdinR); string(left) != nextRequest {
		t.Errorf("the engine's stdin holds %q after the command, want the untouched %q", left, nextRequest)
	}
}
