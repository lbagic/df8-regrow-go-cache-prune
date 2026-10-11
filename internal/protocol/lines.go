package protocol

import (
	"bufio"
	"errors"
	"io"
)

// requestLine is one line of input: its bytes without the newline, or
// tooLong when it exceeded the limit and was discarded.
type requestLine struct {
	data    []byte
	tooLong bool
}

// readLines hands every line of r to deliver until r ends or deliver
// returns false. A line longer than max is skipped to its newline and
// delivered as tooLong, so one oversized request costs at most max
// bytes of memory and the stream stays usable. An unterminated last
// line is delivered too. The error is r's, nil at EOF.
func readLines(r io.Reader, max int, deliver func(requestLine) bool) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var cur requestLine
	for {
		frag, err := br.ReadSlice('\n')
		ended := len(frag) > 0 && frag[len(frag)-1] == '\n'
		if ended {
			frag = frag[:len(frag)-1]
		}
		switch {
		case cur.tooLong:
		case len(cur.data)+len(frag) > max:
			cur = requestLine{tooLong: true}
		default:
			cur.data = append(cur.data, frag...)
		}
		if ended {
			if !deliver(cur) {
				return nil
			}
			cur = requestLine{}
			continue
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if len(cur.data) > 0 || cur.tooLong {
				deliver(cur)
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
