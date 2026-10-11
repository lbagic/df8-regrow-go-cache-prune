package trash

import "fmt"

// PreviewCommand is the exact command Move runs to trash a path:
// Finder handles the move so the OS "Put Back" works, and the script
// returns the trashed item's POSIX path — Finder renames on collision
// ("x 14.54.11"), so the receipt must record where the item actually
// landed, not where we guessed. The plan screen and --json show this
// argv verbatim; execution runs it verbatim.
func PreviewCommand(path string) []string {
	script := fmt.Sprintf(
		"tell application %q to return POSIX path of ((delete POSIX file %q) as alias)",
		"Finder", path)
	return []string{"osascript", "-e", script}
}
