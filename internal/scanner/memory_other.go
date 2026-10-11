//go:build !darwin

package scanner

func hostMemory() int64 { return 0 }
