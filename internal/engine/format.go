package engine

import "fmt"

// HumanBytes renders a byte count the way the product sketch does:
// "20.1 GiB". Lives in engine so every layer (TUI, CLI printers,
// scanner labels) formats sizes identically.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// UnreadableNote says why a size is a lower bound. Full Disk Access is
// the likely cause, not a certain one.
const UnreadableNote = "blocked or unreadable (Full Disk Access may be needed)"

// SizeText renders a measured size: "unreadable" when a lower bound
// holds nothing, "≥ X" when X is a lower bound.
func SizeText(bytes int64, partial bool) string {
	switch {
	case !partial:
		return HumanBytes(bytes)
	case bytes == 0:
		return "unreadable"
	default:
		return "≥ " + HumanBytes(bytes)
	}
}

// PartialText qualifies a size SizeText rendered: empty when the size
// is complete.
func PartialText(bytes int64, partial bool) string {
	switch {
	case !partial:
		return ""
	case bytes == 0:
		return "unreadable: " + UnreadableNote
	default:
		return "some folders unreadable: " + UnreadableNote
	}
}
