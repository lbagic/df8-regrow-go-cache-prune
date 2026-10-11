//go:build darwin

package headroom

import (
	"encoding/binary"
	"fmt"
	"syscall"
)

// swapUsed reads xsw_usage from vm.swapusage: {total, avail, used
// uint64; pagesize uint32; encrypted int32}. syscall.Sysctl treats the
// value as a C string and drops a trailing NUL, so the buffer may come
// back one byte short of the struct; only the first 24 bytes matter.
func swapUsed() (int64, error) {
	raw, err := syscall.Sysctl("vm.swapusage")
	if err != nil {
		return 0, fmt.Errorf("sysctl vm.swapusage: %w", err)
	}
	if len(raw) < 24 {
		return 0, fmt.Errorf("sysctl vm.swapusage: %d bytes, want at least 24", len(raw))
	}
	return int64(binary.LittleEndian.Uint64([]byte(raw[16:24]))), nil
}
