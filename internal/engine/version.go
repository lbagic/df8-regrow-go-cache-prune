package engine

import (
	"strconv"
	"strings"
)

// compareVersion compares dotted numeric versions ("15.2", "26")
// component-wise, returning -1, 0, or 1. Missing components count as
// zero, so "15" == "15.0". Non-numeric components compare as strings,
// which is enough for macOS/Linux release numbering.
func compareVersion(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		av, bv := component(as, i), component(bs, i)
		an, aerr := strconv.Atoi(av)
		bn, berr := strconv.Atoi(bv)
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				return sign(an - bn)
			}
		default:
			if c := strings.Compare(av, bv); c != 0 {
				return c
			}
		}
	}
	return 0
}

func component(parts []string, i int) string {
	if i < len(parts) {
		return parts[i]
	}
	return "0"
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
