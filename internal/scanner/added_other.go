//go:build !darwin

package scanner

import (
	"errors"
	"io/fs"
	"time"
)

func addedTime(path string) (time.Time, error) {
	return time.Time{}, &fs.PathError{Op: "getattrlist", Path: path, Err: errors.ErrUnsupported}
}

func fileTimes(fi fs.FileInfo) []time.Time { return []time.Time{fi.ModTime()} }
