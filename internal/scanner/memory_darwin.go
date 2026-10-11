//go:build darwin

package scanner

import (
	"encoding/binary"
	"syscall"
)

// hostMemory is the machine's physical memory in bytes, 0 when the
// kernel does not say. syscall.Sysctl treats the value as a C string
// and drops a trailing NUL, so the 8-byte integer can come back short.
func hostMemory() int64 {
	raw, err := syscall.Sysctl("hw.memsize")
	if err != nil || len(raw) > 8 {
		return 0
	}
	var buf [8]byte
	copy(buf[:], raw)
	return int64(binary.LittleEndian.Uint64(buf[:]))
}
