// Package jsonl holds what regrow's line journals (the oplog and the
// headroom history) share: appending to a file an earlier write may
// have cut short.
package jsonl

import "os"

// Frame returns line ready to append to f: newline-terminated, and
// starting on a fresh line when f does not end in one, as after a write
// cut short by a full disk. When f cannot be read it assumes the
// fragment: a spare newline costs a blank line, which readers skip,
// while a missing one glues two lines together.
func Frame(f *os.File, line []byte) []byte {
	out := make([]byte, 0, len(line)+2)
	if !endsWithNewline(f) {
		out = append(out, '\n')
	}
	out = append(out, line...)
	return append(out, '\n')
}

func endsWithNewline(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	if info.Size() == 0 {
		return true
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, info.Size()-1); err != nil {
		return false
	}
	return last[0] == '\n'
}
