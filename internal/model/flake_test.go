package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFlakeVerdictLikelyFlakyMarshalsEvidence(t *testing.T) {
	v := FlakeVerdict{
		Verdict: "likely_flaky", Confidence: "high",
		Run:         FlakeRun{ID: 42, HeadSHA: "deadbeef", HeadBranch: "main", Attempt: 2, Workflow: "CI", HTMLURL: "hurl"},
		FailingJobs: []string{"test (ubuntu-latest, 20)"},
		Evidence: []FlakeEvidence{{
			Tier: "same_run_pass_attempt", JobName: "test (ubuntu-latest, 20)",
			Detail: "attempt 1 passed job 'test (ubuntu-latest, 20)'", Attempt: 1, HTMLURL: "jurl",
		}},
		BaseRate:     &FlakeBaseRate{Branch: "main", Runs: 20, Failures: 3, Rate: 0.15, Window: "last 20 runs", Capped: true},
		RunsExamined: 24,
		Window:       FlakeWindow{AttemptsExamined: 1, SiblingsExamined: 3, BranchRuns: 20, BranchWindowCapped: true},
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"verdict":"likely_flaky"`, `"confidence":"high"`,
		`"tier":"same_run_pass_attempt"`, `"job_name":"test (ubuntu-latest, 20)"`,
		`"base_rate"`, `"rate":0.15`, `"runs_examined":24`,
		`"attempts_examined":1`, `"branch_window_capped":true`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
}

func TestFlakeVerdictInsufficientOmitsEvidenceAndBaseRate(t *testing.T) {
	v := FlakeVerdict{
		Verdict: "insufficient_evidence", Confidence: "low",
		Run:          FlakeRun{ID: 42, HeadSHA: "deadbeef", HTMLURL: "hurl"},
		FailingJobs:  []string{"e2e"},
		RunsExamined: 1,
		Window:       FlakeWindow{},
	}
	b, _ := json.Marshal(v)
	s := string(b)
	if strings.Contains(s, `"evidence"`) {
		t.Errorf("empty evidence must be omitted: %s", s)
	}
	if strings.Contains(s, `"base_rate"`) {
		t.Errorf("nil base_rate must be omitted: %s", s)
	}
	// Calibration fields must always render, even on the negative path.
	for _, want := range []string{`"failing_jobs":["e2e"]`, `"runs_examined":1`, `"window"`, `"attempts_examined":0`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
}

// A red run with zero failing jobs (a workflow-file syntax error) must render
// failing_jobs as [] not null, so agents can index it unconditionally.
func TestFlakeVerdictEmptyFailingJobsRendersArray(t *testing.T) {
	v := FlakeVerdict{
		Verdict: "insufficient_evidence", Confidence: "low",
		Run:         FlakeRun{ID: 1, HeadSHA: "x", HTMLURL: "u"},
		FailingJobs: []string{},
		Note:        "run failed with no failing jobs; see html_url",
	}
	b, _ := json.Marshal(v)
	if !strings.Contains(string(b), `"failing_jobs":[]`) {
		t.Errorf("empty failing_jobs must render as []: %s", b)
	}
	if !strings.Contains(string(b), `"note":`) {
		t.Errorf("note must render when set: %s", b)
	}
}
