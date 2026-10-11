package engine

// Doctor mode (Prompt H, plans/2026-07-13-doctor-phantom.md): scan
// only the hero-bug rules and the phantom-space category, run the
// fix-the-cause checks, and assemble the three-section report.
// Selection and assembly are pure functions here; cmd owns printing.

// CategoryPhantomSpace is the category whose rules explain space
// Finder counts but du cannot find (TM snapshots, sparse VM disks,
// purgeable). They ride along in every doctor run.
const CategoryPhantomSpace = "phantom-space"

// DoctorRules selects the catalog subset a doctor run scans: every
// rule carrying a doctor block plus the whole phantom-space category.
func DoctorRules(catalog []Rule) []Rule {
	var out []Rule
	for _, r := range catalog {
		if r.Doctor != nil || r.Category == CategoryPhantomSpace {
			out = append(out, r)
		}
	}
	return out
}

// Verdict is a doctor check's outcome: a hero check against its
// healthy/runaway line, or a cause check against the machine.
type Verdict string

const (
	// VerdictFlagged: the total, even as a lower bound, is over the
	// line — the known bug's signature, worth the story and a fix. For
	// a cause: it is in effect here.
	VerdictFlagged Verdict = "flagged"
	// VerdictNormal: the total is complete and at or under the line.
	// For a cause: not in effect, or what it reads is not on this
	// machine.
	VerdictNormal Verdict = "normal"
	// VerdictUnknown: under the line, but some of it could not be
	// read (or the rule failed), so the real total may be over. For a
	// cause: what it reads could not be read.
	VerdictUnknown Verdict = "unknown"
)

// HeroCheck is one hero-bug verdict: the rule's measured total against
// its healthy/runaway line.
type HeroCheck struct {
	Finding Finding `json:"finding"`
	Verdict Verdict `json:"verdict"`
}

// heroVerdict judges a finding against its doctor line. A lower bound
// over the line is proof enough to flag; under the line it proves
// nothing.
func heroVerdict(f Finding) Verdict {
	switch {
	case f.TotalBytes() > int64(f.Rule.Doctor.FlagAbove):
		return VerdictFlagged
	case f.Err != "" || f.Partial():
		return VerdictUnknown
	default:
		return VerdictNormal
	}
}

// CauseCheck is one fix-the-cause row: whether the cause is in effect
// on this machine.
type CauseCheck struct {
	RuleID  string  `json:"rule_id"`
	Cause   Cause   `json:"cause"`
	Verdict Verdict `json:"verdict"`
	// Detail is what the check saw, in one line: the evidence when
	// flagged, the value or the absence when normal, what could not be
	// read when unknown.
	Detail string `json:"detail,omitempty"`
}

// DoctorReport is the assembled report: hero verdicts in catalog
// order, the fix-the-cause rows, then the phantom-space findings with
// their explainer copy.
type DoctorReport struct {
	Hero    []HeroCheck  `json:"hero"`
	Causes  []CauseCheck `json:"causes"`
	Phantom []Finding    `json:"phantom"`
}

// BuildDoctorReport splits doctor-scan findings into the report and
// carries the cause rows as checked. A rule can appear in both finding
// sections only by carrying a doctor block inside phantom-space; today
// the sections are disjoint.
func BuildDoctorReport(findings []Finding, causes []CauseCheck) DoctorReport {
	rep := DoctorReport{Causes: causes}
	for _, f := range findings {
		if f.Rule.Doctor != nil {
			rep.Hero = append(rep.Hero, HeroCheck{Finding: f, Verdict: heroVerdict(f)})
		}
		if f.Rule.Category == CategoryPhantomSpace {
			rep.Phantom = append(rep.Phantom, f)
		}
	}
	return rep
}
