// Package rules embeds the declarative cleaning-rule catalog.
// Rules are data: community contributions are YAML PRs, not code.
// At runtime `--rules-dir` can override the embedded catalog for
// development and rule testing.
package rules

import "embed"

// FS holds every *.yaml rule embedded at build time.
//
//go:embed *.yaml
var FS embed.FS
