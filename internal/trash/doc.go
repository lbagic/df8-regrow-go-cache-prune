// Package trash owns the trash-not-rm mechanism (ARCHITECTURE.md
// invariant 2): the path guard every destructive target must pass,
// the Finder move to the OS Trash (preview and execution are the same
// argv by construction), the staging-directory fallback for when
// Finder is unavailable, and the receipts that undo renames back.
package trash
