package protocol

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// outbox is the single writer of the event stream. send queues and
// returns at once, so a reader that stops draining stdout blocks only
// the writer goroutine, never a scan, an execute or the request loop.
// The queue is unbounded: a fixed buffer would have to block or drop
// once full, and the backlog is bounded by what the peer asked for.
type outbox struct {
	mu     sync.Mutex
	queue  [][]byte
	closed bool
	// err is the write error that stopped the writer.
	err  error
	wake chan struct{}
	done chan struct{}
}

func newOutbox(w io.Writer) *outbox {
	o := &outbox{wake: make(chan struct{}, 1), done: make(chan struct{})}
	go o.run(w)
	return o
}

// send queues one event as a line. It reports false when the event
// cannot be encoded; nothing is queued then.
func (o *outbox) send(event any) bool {
	line, err := json.Marshal(event)
	if err != nil {
		return false
	}
	line = append(line, '\n')
	o.mu.Lock()
	if !o.closed {
		o.queue = append(o.queue, line)
	}
	o.mu.Unlock()
	o.signal()
	return true
}

func (o *outbox) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

func (o *outbox) run(w io.Writer) {
	defer close(o.done)
	for {
		o.mu.Lock()
		batch, closed := o.queue, o.closed
		o.queue = nil
		o.mu.Unlock()
		if len(batch) == 0 {
			if closed {
				return
			}
			<-o.wake
			continue
		}
		for _, line := range batch {
			if _, err := w.Write(line); err != nil {
				// The reader is gone: stop queueing for it.
				o.mu.Lock()
				o.closed, o.queue, o.err = true, nil, err
				o.mu.Unlock()
				return
			}
		}
	}
}

// writeErr is the write error that stopped the writer, if one did.
func (o *outbox) writeErr() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}

// close stops accepting events and waits up to grace for the queued
// ones to be written; a grace of 0 waits until they are. It reports
// whether the queue drained.
func (o *outbox) close(grace time.Duration) bool {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	o.signal()
	if grace == 0 {
		<-o.done
		return true
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-o.done:
		return true
	case <-timer.C:
		return false
	}
}
