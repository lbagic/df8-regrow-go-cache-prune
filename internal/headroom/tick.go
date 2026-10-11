package headroom

// Tick is one pass of the watch loop: the sample, the forecast when
// there is one, the alerts that crossed, and what autotrim did.
type Tick struct {
	Sample
	DaysToFull *float64     `json:"days_to_full,omitempty"`
	Alerts     []Alert      `json:"alerts,omitempty"`
	Pruned     *PruneResult `json:"pruned,omitempty"`
}

// PruneResult is what a prune did, or why it did not run.
type PruneResult struct {
	RuleID string `json:"rule_id"`
	// Skipped says why nothing ran; every field below is then zero.
	Skipped string `json:"skipped,omitempty"`
	// Run is the oplog run that journaled the prune.
	Run   string `json:"run,omitempty"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
	// FreeBefore and FreeAfter are statfs readings around the prune,
	// kept apart from Bytes: Time Machine snapshots can hold the freed
	// blocks, so free space may not move by what was deleted.
	FreeBefore int64  `json:"free_before"`
	FreeAfter  int64  `json:"free_after"`
	Error      string `json:"error,omitempty"`
}
