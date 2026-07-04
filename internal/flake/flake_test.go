package flake

import (
	"testing"

	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/model"
)

func job(name, concl string) JobConclusion { return JobConclusion{Name: name, Conclusion: concl} }

const jobName = "test (ubuntu-latest, 20)"

func TestDecide(t *testing.T) {
	cases := []struct {
		name         string
		ev           Evidence
		wantVerdict  string
		wantConf     string
		wantEvidTier string // "" = expect no evidence
	}{
		{
			name: "same-run pass attempt is divergence",
			ev: Evidence{
				Workflow: "CI", Event: "push", RunAttempt: 2,
				TargetJobs:    []JobConclusion{job(jobName, "failure")},
				PriorAttempts: []Attempt{{Number: 1, Jobs: []JobConclusion{job(jobName, "success")}}},
			},
			wantVerdict: VerdictLikelyFlaky, wantConf: "high", wantEvidTier: tierSameRunPass,
		},
		{
			name: "same-sha sibling pass (same workflow+event) is divergence",
			ev: Evidence{
				Workflow: "CI", Event: "push",
				TargetJobs: []JobConclusion{job(jobName, "failure")},
				Siblings:   []SiblingRun{{ID: 7, Workflow: "CI", Event: "push", Conclusion: "success", Jobs: []JobConclusion{job(jobName, "success")}}},
			},
			wantVerdict: VerdictLikelyFlaky, wantConf: "high", wantEvidTier: tierSameSHASibling,
		},
		{
			name: "sibling with different workflow does not count",
			ev: Evidence{
				Workflow: "CI", Event: "push",
				TargetJobs: []JobConclusion{job(jobName, "failure")},
				Siblings:   []SiblingRun{{ID: 7, Workflow: "Lint", Event: "push", Jobs: []JobConclusion{job(jobName, "success")}}},
			},
			wantVerdict: VerdictInsufficient, wantConf: "low",
		},
		{
			name: "sibling with different event does not count",
			ev: Evidence{
				Workflow: "CI", Event: "push",
				TargetJobs: []JobConclusion{job(jobName, "failure")},
				Siblings:   []SiblingRun{{ID: 7, Workflow: "CI", Event: "pull_request", Jobs: []JobConclusion{job(jobName, "success")}}},
			},
			wantVerdict: VerdictInsufficient, wantConf: "low",
		},
		{
			name: "matrix name mismatch does not count",
			ev: Evidence{
				Workflow: "CI", Event: "push",
				TargetJobs: []JobConclusion{job(jobName, "failure")},
				Siblings:   []SiblingRun{{ID: 7, Workflow: "CI", Event: "push", Jobs: []JobConclusion{job("test (ubuntu-latest, 22)", "success")}}},
			},
			wantVerdict: VerdictInsufficient, wantConf: "low",
		},
		{
			name: "branch base-rate alone stays insufficient",
			ev: Evidence{
				Workflow: "CI", Event: "push",
				TargetJobs: []JobConclusion{job(jobName, "failure")},
				Branch:     BranchHistory{Branch: "main", Runs: 20, Failures: 8, Window: "last 20 runs"},
			},
			wantVerdict: VerdictInsufficient, wantConf: "low",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := Decide(c.ev)
			if v.Verdict != c.wantVerdict {
				t.Errorf("verdict = %q, want %q", v.Verdict, c.wantVerdict)
			}
			if v.Confidence != c.wantConf {
				t.Errorf("confidence = %q, want %q", v.Confidence, c.wantConf)
			}
			if c.wantEvidTier == "" {
				if len(v.Evidence) != 0 {
					t.Errorf("expected no evidence, got %+v", v.Evidence)
				}
			} else {
				if len(v.Evidence) == 0 {
					t.Fatalf("expected evidence tier %q, got none", c.wantEvidTier)
				}
				if v.Evidence[0].Tier != c.wantEvidTier {
					t.Errorf("evidence tier = %q, want %q", v.Evidence[0].Tier, c.wantEvidTier)
				}
				if v.Evidence[0].JobName != jobName {
					t.Errorf("evidence job_name = %q, want %q", v.Evidence[0].JobName, jobName)
				}
			}
		})
	}
}

func TestDecideSiblingEventRecorded(t *testing.T) {
	v := Decide(Evidence{
		Workflow: "CI", Event: "push",
		TargetJobs: []JobConclusion{job(jobName, "failure")},
		Siblings:   []SiblingRun{{ID: 7, Workflow: "CI", Event: "push", Jobs: []JobConclusion{job(jobName, "success")}}},
	})
	if len(v.Evidence) == 0 || v.Evidence[0].SiblingRunID != 7 || v.Evidence[0].SiblingEvent != "push" {
		t.Errorf("sibling evidence must record run id and event: %+v", v.Evidence)
	}
}

func TestDecideBranchBaseRateAttached(t *testing.T) {
	v := Decide(Evidence{
		Workflow: "CI", Event: "push",
		TargetJobs: []JobConclusion{job("e2e", "failure")},
		Branch:     BranchHistory{Branch: "main", Runs: 20, Failures: 5, Window: "last 20 runs", Capped: true},
	})
	if v.BaseRate == nil {
		t.Fatalf("base_rate must be attached when branch history was fetched")
	}
	if v.BaseRate.Rate != 0.25 {
		t.Errorf("rate = %v, want 0.25", v.BaseRate.Rate)
	}
	if !v.Window.BranchWindowCapped {
		t.Errorf("window.branch_window_capped should reflect Branch.Capped")
	}
}

// base_rate must attach on the likely_flaky path too (free context; it never
// escalates the verdict, only informs).
func TestDecideBaseRateOnLikelyFlakyPath(t *testing.T) {
	v := Decide(Evidence{
		Workflow: "CI", Event: "push", RunAttempt: 2,
		TargetJobs:    []JobConclusion{job(jobName, "failure")},
		PriorAttempts: []Attempt{{Number: 1, Jobs: []JobConclusion{job(jobName, "success")}}},
		Branch:        BranchHistory{Branch: "main", Runs: 10, Failures: 1, Window: "last 10 runs"},
	})
	if v.Verdict != VerdictLikelyFlaky {
		t.Fatalf("verdict = %q, want likely_flaky", v.Verdict)
	}
	if v.BaseRate == nil {
		t.Errorf("base_rate should be present on the likely_flaky path too")
	}
}

func TestDecideRunsExaminedAccounting(t *testing.T) {
	v := Decide(Evidence{
		Workflow: "CI", Event: "push",
		TargetJobs:    []JobConclusion{job("a", "failure")},
		PriorAttempts: []Attempt{{Number: 1}, {Number: 2}},
		Siblings:      []SiblingRun{{ID: 1}, {ID: 2}, {ID: 3}},
		Branch:        BranchHistory{Runs: 20},
	})
	if want := 1 + 2 + 3 + 20; v.RunsExamined != want {
		t.Errorf("runs_examined = %d, want %d", v.RunsExamined, want)
	}
	if v.Window.AttemptsExamined != 2 || v.Window.SiblingsExamined != 3 || v.Window.BranchRuns != 20 {
		t.Errorf("window = %+v", v.Window)
	}
}

func TestDecideRedRunZeroFailingJobs(t *testing.T) {
	v := Decide(Evidence{
		Workflow: "CI", Event: "push", HTMLURL: "hurl",
		TargetJobs: []JobConclusion{job("build", "success")}, // red run, but no failing job
	})
	if v.Verdict != VerdictInsufficient {
		t.Errorf("verdict = %q, want insufficient", v.Verdict)
	}
	if len(v.FailingJobs) != 0 {
		t.Errorf("failing_jobs = %+v, want empty", v.FailingJobs)
	}
	if v.Note == "" {
		t.Errorf("expected a note pointing at html_url")
	}
}

func TestDecideFailingJobsNonNil(t *testing.T) {
	if v := Decide(Evidence{Workflow: "CI", Event: "push", HTMLURL: "u"}); v.FailingJobs == nil {
		t.Errorf("failing_jobs must be non-nil so it renders as []")
	}
}

func TestExitCode(t *testing.T) {
	cases := map[string]core.Code{
		VerdictLikelyFlaky:  core.CodeOK,
		VerdictInsufficient: core.CodeOK,
		"garbage":           core.CodeAPI,
	}
	for verdict, want := range cases {
		if got := ExitCode(model.FlakeVerdict{Verdict: verdict}); got != want {
			t.Errorf("ExitCode(%q) = %v, want %v", verdict, got, want)
		}
	}
}

func TestDefault(t *testing.T) {
	if d := Default(); d.MaxSiblings != DefaultMaxSiblings || d.BranchWindow != DefaultBranchWindow {
		t.Errorf("Default() = %+v", d)
	}
}
