// Package engine loads the declarative rule catalog and turns scan
// results into an executable, dry-run-first plan.
//
// See docs/PRODUCT.md §6 for the rule schema: {id, title, risk,
// per-OS paths (version-aware), marker discovery, native command,
// regen story, sudo}.
package engine
