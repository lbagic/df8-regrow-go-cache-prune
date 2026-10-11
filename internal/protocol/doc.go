// Package protocol is the engine protocol behind `regrow engine`: JSON
// lines over stdin and stdout, through which a shell (the menubar app)
// scans, plans and executes without a terminal. docs/ENGINE.md is the
// contract; this package implements it across injected seams for the
// scan, the executor and the journal.
package protocol
