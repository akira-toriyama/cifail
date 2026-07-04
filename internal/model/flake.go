package model

// FlakeVerdict is the JSON `cifail flake` prints: a 1-shot, evidence-tiered
// judgement of whether a red run is likely flaky (rerun it) or lacks the
// evidence to say so (debug it). Verdict is the load-bearing field an agent
// branches on; Confidence is "high" only when same-sha divergence proves the
// flake. Both verdicts exit 0 — a produced judgement is a successful output.
type FlakeVerdict struct {
	Verdict     string          `json:"verdict"`    // likely_flaky | insufficient_evidence
	Confidence  string          `json:"confidence"` // high | low
	Run         FlakeRun        `json:"run"`
	FailingJobs []string        `json:"failing_jobs"` // always present ([] not null)
	Evidence    []FlakeEvidence `json:"evidence,omitempty"`
	BaseRate    *FlakeBaseRate  `json:"base_rate,omitempty"`
	// RunsExamined is target + prior attempts + siblings + branch window, so the
	// agent can calibrate how much evidence actually backed the verdict.
	RunsExamined int         `json:"runs_examined"`
	Window       FlakeWindow `json:"window"`
	Note         string      `json:"note,omitempty"`
}

// FlakeRun identifies the red run under judgement.
type FlakeRun struct {
	ID         int64  `json:"id"`
	HeadSHA    string `json:"head_sha"`
	HeadBranch string `json:"head_branch,omitempty"`
	Attempt    int    `json:"run_attempt,omitempty"`
	Workflow   string `json:"workflow,omitempty"`
	HTMLURL    string `json:"html_url"`
}

// FlakeEvidence is one proof of same-sha divergence: a failing job that passed
// on another attempt of this run, or on a sibling run of the same head sha. Its
// presence (any at all) is the sole gate that escalates the verdict to
// likely_flaky — branch base rate never does.
type FlakeEvidence struct {
	Tier         string `json:"tier"` // same_run_pass_attempt | same_sha_sibling_pass
	JobName      string `json:"job_name"`
	Detail       string `json:"detail"`
	Attempt      int    `json:"attempt,omitempty"`        // same_run_pass_attempt: the passing attempt
	SiblingRunID int64  `json:"sibling_run_id,omitempty"` // same_sha_sibling_pass
	SiblingEvent string `json:"sibling_event,omitempty"`  // same_sha_sibling_pass
	HTMLURL      string `json:"html_url,omitempty"`
}

// FlakeBaseRate is the weak branch-history tier: how often this branch's recent
// runs failed. It is informational only — it never escalates the verdict or
// confidence on its own, so a red run on a flaky branch still needs same-sha
// divergence to be called likely_flaky.
type FlakeBaseRate struct {
	Branch   string  `json:"branch"`
	Runs     int     `json:"runs"`
	Failures int     `json:"failures"`
	Rate     float64 `json:"rate"`
	Window   string  `json:"window"`
	Capped   bool    `json:"capped,omitempty"`
}

// FlakeWindow reports the survey breadth behind the verdict so an agent can see
// how much was actually examined (and whether the branch window was truncated).
type FlakeWindow struct {
	AttemptsExamined   int  `json:"attempts_examined"`
	SiblingsExamined   int  `json:"siblings_examined"`
	BranchRuns         int  `json:"branch_runs"`
	BranchWindowCapped bool `json:"branch_window_capped,omitempty"`
}
