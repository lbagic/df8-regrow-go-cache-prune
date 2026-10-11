//go:build !darwin

package headroom

func swapUsed() (int64, error) { return 0, nil }
