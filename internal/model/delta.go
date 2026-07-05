package model

// DeltaReport is the JSON `cifail delta` prints: the input delta between a
// failing run and the last green run of the same workflow+branch. LastGreen is
// the load-bearing field an agent branches on first, so it renders an explicit
// null when no green baseline exists; optional context blocks (CommitRange,
// Environment) are omitted instead. Any produced report exits 0 — degraded
// reports carry a Note saying what is missing and why.
type DeltaReport struct {
	Failing    DeltaRun  `json:"failing"`
	LastGreen  *DeltaRun `json:"last_green"` // explicit null when absent
	SameCommit bool      `json:"same_commit"`
	// CommitRange is omitted when the runs share a commit (git has no answer —
	// see SameCommit) or when no comparison could be made (Note explains).
	CommitRange *DeltaCommitRange `json:"commit_range,omitempty"`
	// Environment is omitted when either run's logs are unavailable (expired
	// retention — Note explains); it carries the drift git diffs cannot see.
	Environment *DeltaEnvironment `json:"environment,omitempty"`
	Jobs        DeltaJobs         `json:"jobs"`
	Budget      DeltaBudget       `json:"budget"`
	Note        string            `json:"note,omitempty"`
}

// DeltaRun identifies one side of the comparison.
type DeltaRun struct {
	Run     int64  `json:"run"`
	SHA     string `json:"sha"`
	Attempt int    `json:"attempt,omitempty"`
	// AgeDays is how many days before "now" the green run started — how stale
	// the baseline is. Only set on last_green.
	AgeDays int    `json:"age_days,omitempty"`
	URL     string `json:"url,omitempty"`
}

// DeltaCommitRange summarises green...failing: commit counts and the changed
// files classified into agent-actionable buckets. The arrays are always
// present ([] not null) — an empty bucket is a finding.
type DeltaCommitRange struct {
	AheadBy  int `json:"ahead_by"`
	BehindBy int `json:"behind_by"` // >0: rebase/force-push hid history
	// FilesChanged is the number of files LISTED by the compare API; when
	// FilesCapped is true the true count may be higher (API caps at 300).
	FilesChanged    int                   `json:"files_changed"`
	FilesCapped     bool                  `json:"files_capped,omitempty"`
	TopDirs         []string              `json:"top_dirs"` // "dir (N)", largest first
	Lockfiles       []DeltaLockfile       `json:"lockfiles"`
	WorkflowChanges []DeltaWorkflowChange `json:"workflow_changes"`
}

// DeltaLockfile is a changed dependency lockfile — v1 detects WHICH moved and
// by how many lines; it does not parse version-level changes.
type DeltaLockfile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// DeltaWorkflowChange is a changed workflow file, with any `uses:` refs that
// moved in its patch ("owner/repo old→new").
type DeltaWorkflowChange struct {
	Path        string   `json:"path"`
	UsesChanged []string `json:"uses_changed"`
}

// DeltaEnvironment is the drift no text diff can catch: what the runs' 'Set up
// job' logs recorded as the RESOLVED action SHAs and runner image.
type DeltaEnvironment struct {
	Actions []DeltaActionDrift `json:"actions"`
	Runner  *DeltaRunnerDrift  `json:"runner,omitempty"`
}

// DeltaActionDrift compares one action ref's resolved SHA across the two runs.
// A missing side (the run didn't download that action) leaves its SHA empty
// and Drifted false — absence of proof, not proof of drift.
type DeltaActionDrift struct {
	Ref        string `json:"ref"` // as written in the workflow, e.g. "actions/checkout@v4"
	GreenSHA   string `json:"green_sha,omitempty"`
	FailingSHA string `json:"failing_sha,omitempty"`
	Drifted    bool   `json:"drifted"`
}

// DeltaRunnerDrift compares the runner image ("image/version") across the runs.
type DeltaRunnerDrift struct {
	Green   string `json:"green"`
	Failing string `json:"failing"`
	Drifted bool   `json:"drifted"`
}

// DeltaJobs maps the input delta to its observable effect: jobs failing now
// whose same-named job passed in the green run. Always present ([] not null).
type DeltaJobs struct {
	NewlyFailing []string `json:"newly_failing"`
}

// DeltaBudget reports how the byte budget was spent over the report's
// variable-length lists, so the caller knows whether the survey is complete.
type DeltaBudget struct {
	LimitBytes   int `json:"limit_bytes"`
	UsedBytes    int `json:"used_bytes"`
	OmittedItems int `json:"omitted_items"`
}
