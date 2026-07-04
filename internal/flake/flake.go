// Package flake decides — purely and without any IO — whether a red GitHub
// Actions run is likely flaky (rerun it) or lacks the evidence to say so (debug
// it). The cli adapter gathers the run history (all IO lives there) into an
// Evidence snapshot; Decide reasons over it and ExitCode maps the result to the
// process exit-code contract. Keep this package dependency-free (stdlib + model
// + core) so the verdict logic stays unit-testable with no network.
package flake

import (
	"fmt"

	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/model"
)

// Verdict strings (the load-bearing field an agent branches on) and evidence
// tiers, named so Decide and its tests can't drift on a literal.
const (
	VerdictLikelyFlaky  = "likely_flaky"
	VerdictInsufficient = "insufficient_evidence"

	confidenceHigh = "high"
	confidenceLow  = "low"

	tierSameRunPass    = "same_run_pass_attempt"
	tierSameSHASibling = "same_sha_sibling_pass"
)

// Config bounds how much history the cli adapter gathers before calling Decide.
// Decide itself consumes none of it — the breadth is already baked into the
// Evidence — but it is the feature's knob namespace, and the cli seeds its flag
// defaults from Default() (so --help shows the real defaults).
type Config struct {
	MaxSiblings  int
	BranchWindow int
}

const (
	DefaultMaxSiblings  = 10
	DefaultBranchWindow = 20
)

// Default returns the standard survey breadth.
func Default() Config {
	return Config{MaxSiblings: DefaultMaxSiblings, BranchWindow: DefaultBranchWindow}
}

// JobConclusion is one job's terminal state. Job identity everywhere in this
// package is the exact display name (the matrix identity, e.g.
// "test (ubuntu-latest, 20)").
type JobConclusion struct {
	Name       string
	Conclusion string
	HTMLURL    string
}

// Attempt is the jobs of one prior attempt of the target run.
type Attempt struct {
	Number int
	Jobs   []JobConclusion
}

// SiblingRun is another workflow run on the SAME head sha as the target.
type SiblingRun struct {
	ID         int64
	Workflow   string
	Event      string
	Conclusion string
	HTMLURL    string
	Jobs       []JobConclusion
}

// BranchHistory is the weak base-rate tier: how often the branch's recent runs
// failed. Informational only.
type BranchHistory struct {
	Branch   string
	Runs     int
	Failures int
	Window   string
	Capped   bool
}

// Evidence is the materialized run history Decide reasons over — gathered by the
// cli adapter (all IO), decided here (no IO).
type Evidence struct {
	RunID      int64
	HeadSHA    string
	HeadBranch string
	Workflow   string
	Event      string
	RunAttempt int
	HTMLURL    string

	TargetJobs    []JobConclusion // latest attempt, every conclusion
	PriorAttempts []Attempt       // attempts 1..RunAttempt-1
	Siblings      []SiblingRun    // same head sha, target run excluded
	Branch        BranchHistory
}

// Decide turns gathered Evidence into a verdict. The rule is deliberately
// narrow: a run is likely_flaky ONLY when a failing job diverged on the same
// commit — it passed on a prior attempt of this run (Tier A, strongest: same
// commit so matrix names cannot drift) or on a sibling run of the same head sha
// under the same workflow and event (Tier B). Any such evidence flips the
// verdict; branch base rate never does — it is reported for calibration only.
func Decide(ev Evidence) model.FlakeVerdict {
	// Failing job names in target order, deduped — this is failing_jobs and the
	// set Decide hunts for divergence on. Non-nil so it renders as [].
	failing := make([]string, 0)
	seen := map[string]bool{}
	for _, j := range ev.TargetJobs {
		if j.Conclusion == "failure" && !seen[j.Name] {
			seen[j.Name] = true
			failing = append(failing, j.Name)
		}
	}

	var evidence []model.FlakeEvidence
	for _, name := range failing {
		// Tier A: a prior attempt of THIS run passed the same job (same commit).
		for _, att := range ev.PriorAttempts {
			if pj, ok := findPass(att.Jobs, name); ok {
				evidence = append(evidence, model.FlakeEvidence{
					Tier:    tierSameRunPass,
					JobName: name,
					Detail:  fmt.Sprintf("attempt %d passed job %q", att.Number, name),
					Attempt: att.Number,
					HTMLURL: pj.HTMLURL,
				})
				break
			}
		}
		// Tier B: a sibling run on the SAME head sha passed it. Require the same
		// workflow AND event — a push-vs-pull_request pair on one sha can behave
		// differently (env-gated), so crossing events isn't proof of flakiness.
		for _, sib := range ev.Siblings {
			if sib.Workflow != ev.Workflow || sib.Event != ev.Event {
				continue
			}
			if pj, ok := findPass(sib.Jobs, name); ok {
				evidence = append(evidence, model.FlakeEvidence{
					Tier:         tierSameSHASibling,
					JobName:      name,
					Detail:       fmt.Sprintf("sibling run %d (%s) passed job %q", sib.ID, sib.Event, name),
					SiblingRunID: sib.ID,
					SiblingEvent: sib.Event,
					HTMLURL:      firstNonEmpty(pj.HTMLURL, sib.HTMLURL),
				})
				break
			}
		}
	}

	v := model.FlakeVerdict{
		Verdict:    VerdictInsufficient,
		Confidence: confidenceLow,
		Run: model.FlakeRun{
			ID: ev.RunID, HeadSHA: ev.HeadSHA, HeadBranch: ev.HeadBranch,
			Attempt: ev.RunAttempt, Workflow: ev.Workflow, HTMLURL: ev.HTMLURL,
		},
		FailingJobs:  failing,
		Evidence:     evidence,
		RunsExamined: 1 + len(ev.PriorAttempts) + len(ev.Siblings) + ev.Branch.Runs,
		Window: model.FlakeWindow{
			AttemptsExamined:   len(ev.PriorAttempts),
			SiblingsExamined:   len(ev.Siblings),
			BranchRuns:         ev.Branch.Runs,
			BranchWindowCapped: ev.Branch.Capped,
		},
	}

	// The single honesty gate.
	if len(evidence) > 0 {
		v.Verdict = VerdictLikelyFlaky
		v.Confidence = confidenceHigh
	}

	// Base rate is free context on both paths; it never escalates the verdict.
	if ev.Branch.Runs > 0 {
		v.BaseRate = &model.FlakeBaseRate{
			Branch:   ev.Branch.Branch,
			Runs:     ev.Branch.Runs,
			Failures: ev.Branch.Failures,
			Rate:     float64(ev.Branch.Failures) / float64(ev.Branch.Runs),
			Window:   ev.Branch.Window,
			Capped:   ev.Branch.Capped,
		}
	}

	// A red run with zero failing jobs is a workflow-file / startup failure: no
	// step logs and no divergence to find — point the agent at the run page.
	if len(failing) == 0 {
		v.Note = "run failed with no failing jobs (likely a workflow-file or startup error); open html_url"
	}

	return v
}

// findPass returns the first job in jobs with the given name that concluded
// success.
func findPass(jobs []JobConclusion, name string) (JobConclusion, bool) {
	for _, j := range jobs {
		if j.Name == name && j.Conclusion == "success" {
			return j, true
		}
	}
	return JobConclusion{}, false
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ExitCode maps a verdict to the process exit code. Both real verdicts exit 0 —
// a produced judgement is a successful output; the agent branches on the JSON
// `verdict` field. A default catches a typo'd verdict string as an internal bug.
func ExitCode(v model.FlakeVerdict) core.Code {
	switch v.Verdict {
	case VerdictLikelyFlaky, VerdictInsufficient:
		return core.CodeOK
	default:
		return core.CodeAPI
	}
}
