// Package delta computes — purely and without any IO — the input delta between
// a failing workflow run and the last green run of the same workflow+branch:
// commit range, lockfile and workflow-file changes, resolved action SHAs, and
// runner image. The cli adapter gathers an Evidence snapshot (all IO lives
// there); Build reasons over it. Keep this package dependency-free (stdlib +
// model) so the report logic stays unit-testable with no network.
package delta

import "time"

// DefaultBudgetBytes bounds the report's variable-length lists. Smaller than
// root's 8 KiB log budget: delta's lists are structured summaries, not logs.
const DefaultBudgetBytes = 4096

// Config is the feature's knob namespace; the cli seeds its flag defaults from
// Default() so --help shows the real values.
type Config struct {
	BudgetBytes int
}

func Default() Config {
	return Config{BudgetBytes: DefaultBudgetBytes}
}

// RunMeta identifies one side of the comparison. StartedAt is used only for
// the green run's age; a zero value simply omits age_days.
type RunMeta struct {
	ID        int64
	SHA       string
	Attempt   int
	URL       string
	StartedAt time.Time
}

// JobOutcome is one job's terminal state. Job identity is the exact display
// name (the matrix identity), as everywhere in cifail.
type JobOutcome struct {
	Name       string
	Conclusion string
}

// FileChange is one changed file from the commit comparison.
type FileChange struct {
	Path      string
	Additions int
	Deletions int
	Patch     string
}

// Comparison is the green...failing commit comparison. Capped reports the
// compare API's file-listing limit was hit (survey may be incomplete).
type Comparison struct {
	AheadBy  int
	BehindBy int
	Files    []FileChange
	Capped   bool
}

// SetupLog is one job's 'Set up job' step text (or the head of its whole-job
// log when the per-step file is absent from the archive).
type SetupLog struct {
	Job  string
	Text string
}

// Evidence is the materialized snapshot Build reasons over — gathered by the
// cli adapter (all IO), decided here (no IO). Nil/empty optional fields mean
// the data was unavailable; the *Note fields say why, verbatim for the report.
type Evidence struct {
	Failing RunMeta
	Green   *RunMeta // nil: no green run of this workflow on the branch
	Now     time.Time

	Compare     *Comparison // nil: same commit, no green, or comparison degraded
	CompareNote string      // set when Compare degraded (e.g. force-push 404)

	FailingSetup []SetupLog
	GreenSetup   []SetupLog
	EnvNote      string // set when either side's logs were unavailable

	FailingJobs []JobOutcome
	GreenJobs   []JobOutcome
}
