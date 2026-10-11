//go:build darwin

package scanner

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"syscall"
	"time"
	"unsafe"
)

// getattrlist(2) bits, from <sys/attr.h>.
const (
	attrBitMapCount     = 5
	attrCmnReturnedAttr = 0x80000000
	attrCmnAddedTime    = 0x10000000
)

// attrList is struct attrlist.
type attrList struct {
	bitmapCount uint16
	reserved    uint16
	commonAttr  uint32
	volAttr     uint32
	dirAttr     uint32
	fileAttr    uint32
	forkAttr    uint32
}

var errNoAddedTime = errors.New("the filesystem keeps no date added")

// addedTime is the file's date added: when it was put in the folder
// that holds it. Copying an app into /Applications sets it; the copy's
// own times keep the vendor's build dates.
func addedTime(path string) (time.Time, error) {
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return time.Time{}, err
	}
	req := attrList{bitmapCount: attrBitMapCount, commonAttr: attrCmnReturnedAttr | attrCmnAddedTime}
	// u_int32_t length, attribute_set_t (five u_int32_t), struct timespec.
	var buf [4 + 20 + 16]byte
	_, _, errno := syscall.Syscall6(syscall.SYS_GETATTRLIST, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&req)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
	if errno != 0 {
		return time.Time{}, &fs.PathError{Op: "getattrlist", Path: path, Err: errno}
	}
	length := binary.LittleEndian.Uint32(buf[0:4])
	returned := binary.LittleEndian.Uint32(buf[4:8])
	if returned&attrCmnAddedTime == 0 || length < uint32(len(buf)) {
		return time.Time{}, &fs.PathError{Op: "getattrlist", Path: path, Err: errNoAddedTime}
	}
	sec := int64(binary.LittleEndian.Uint64(buf[24:32]))
	nsec := int64(binary.LittleEndian.Uint64(buf[32:40]))
	return time.Unix(sec, nsec), nil
}

// fileTimes are the times stat reports for a file: modified, changed
// and created.
func fileTimes(fi fs.FileInfo) []time.Time {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return []time.Time{fi.ModTime()}
	}
	return []time.Time{
		fi.ModTime(),
		time.Unix(st.Ctimespec.Unix()),
		time.Unix(st.Birthtimespec.Unix()),
	}
}
