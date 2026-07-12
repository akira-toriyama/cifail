package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/flake"
	"github.com/akira-toriyama/cifail/internal/gh"
	"github.com/akira-toriyama/cifail/internal/model"
)

func TestRunFlakeRejectsBadEntry(t *testing.T) {
	cases := []struct {
		name         string
		args         []string
		pr           int
		branchWindow int
		maxSiblings  int
	}{
		{"both run-id and pr", []string{"123"}, 5, 20, 10},
		{"neither run-id nor pr", nil, 0, 20, 10},
		{"non-integer run-id", []string{"abc"}, 0, 20, 10},
		{"non-positive run-id", []string{"0"}, 0, 20, 10},
		{"non-positive branch-window", []string{"123"}, 0, 0, 10},
		{"non-positive max-siblings", []string{"123"}, 0, 20, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withFlakeFlags(t)
			flakePR, flakeBranchWindow, flakeMaxSiblings = tc.pr, tc.branchWindow, tc.maxSiblings
			err := runFlake(nil, tc.args)
			var ce *core.Error
			if !errors.As(err, &ce) || ce.Code != core.CodeUsage {
				t.Fatalf("want usage error, got %v", err)
			}
		})
	}
}

func TestBuildEvidence(t *testing.T) {
	run := model.Run{ID: 100, HeadSHA: "sha", HeadBranch: "main", Name: "CI", Event: "push", Attempt: 2, HTMLURL: "hurl"}
	f := fakeProber{
		allJobs: map[int64][]gh.JobResult{
			100: {{Name: "test (ubuntu-latest, 20)", Conclusion: "failure"}, {Name: "lint", Conclusion: "success"}},
			200: {{Name: "test (ubuntu-latest, 20)", Conclusion: "success"}},
		},
		attemptJobs: map[string][]gh.JobResult{
			"100/1": {{Name: "test (ubuntu-latest, 20)", Conclusion: "success"}},
		},
		siblings: []gh.RunSummary{
			{ID: 100, Name: "CI", Status: "completed", Conclusion: "failure", Event: "push"}, // the target itself
			{ID: 200, Name: "CI", Status: "completed", Conclusion: "success", Event: "push"},
			{ID: 300, Name: "CI", Status: "in_progress"}, // not completed
		},
		branchRuns: []gh.RunSummary{
			{Status: "completed", Conclusion: "failure"},
			{Status: "completed", Conclusion: "success"},
			{Status: "completed", Conclusion: "failure"},
			{Status: "in_progress"},
		},
		branchCapped: true,
	}
	ev, err := buildEvidence(context.Background(), f, run, flake.Config{MaxSiblings: 10, BranchWindow: 4})
	if err != nil {
		t.Fatalf("buildEvidence: %v", err)
	}
	if ev.RunID != 100 || ev.Workflow != "CI" || ev.Event != "push" {
		t.Errorf("run fields: %+v", ev)
	}
	if len(ev.TargetJobs) != 2 {
		t.Errorf("target jobs = %d, want 2 (passing job kept)", len(ev.TargetJobs))
	}
	if len(ev.PriorAttempts) != 1 || ev.PriorAttempts[0].Number != 1 {
		t.Errorf("prior attempts = %+v, want one (attempt 1)", ev.PriorAttempts)
	}
	if len(ev.Siblings) != 1 || ev.Siblings[0].ID != 200 {
		t.Errorf("siblings = %+v, want just run 200 (target + in-progress excluded)", ev.Siblings)
	}
	if ev.Branch.Runs != 3 || ev.Branch.Failures != 2 {
		t.Errorf("branch base rate = %d/%d, want 2 failures / 3 completed", ev.Branch.Failures, ev.Branch.Runs)
	}
	if !ev.Branch.Capped {
		t.Errorf("branch capped flag not propagated")
	}
	// Sanity: attempt 1 and sibling 200 both passed the failing job -> likely_flaky.
	if v := flake.Decide(ev); v.Verdict != flake.VerdictLikelyFlaky {
		t.Errorf("decided %q, want likely_flaky", v.Verdict)
	}
}

// End-to-end guard for the workflow_id gate: a same-sha sibling from a DIFFERENT
// workflow that happens to share the display name must NOT count as divergence.
// This is the case that FAILS under the old name-matching gate (both named "CI")
// and passes only when the gate keys on workflow_id.
func TestBuildEvidenceSiblingMatchedByWorkflowID(t *testing.T) {
	const failing = "test (ubuntu-latest, 20)"
	run := model.Run{ID: 1, WorkflowID: 42, HeadSHA: "sha", HeadBranch: "main", Name: "CI", Event: "push", Attempt: 1}
	f := fakeProber{
		allJobs: map[int64][]gh.JobResult{
			1: {{Name: failing, Conclusion: "failure"}},
			2: {{Name: failing, Conclusion: "success"}}, // the sibling "passed" the failing job
		},
		siblings: []gh.RunSummary{
			// Same display name "CI", DIFFERENT workflow id -> a different workflow.
			{ID: 2, WorkflowID: 99, Name: "CI", Status: "completed", Conclusion: "success", Event: "push"},
		},
	}
	cfg := flake.Config{MaxSiblings: 10, BranchWindow: 20}

	ev, err := buildEvidence(context.Background(), f, run, cfg)
	if err != nil {
		t.Fatalf("buildEvidence: %v", err)
	}
	if v := flake.Decide(ev); v.Verdict != flake.VerdictInsufficient {
		t.Fatalf("verdict = %q, want insufficient — a same-named but different-workflow sibling must not count", v.Verdict)
	}

	// Same sha, SAME workflow id -> genuine same-workflow divergence -> likely_flaky.
	f.siblings[0].WorkflowID = 42
	ev, err = buildEvidence(context.Background(), f, run, cfg)
	if err != nil {
		t.Fatalf("buildEvidence: %v", err)
	}
	if v := flake.Decide(ev); v.Verdict != flake.VerdictLikelyFlaky {
		t.Fatalf("verdict = %q, want likely_flaky for a same-workflow-id sibling", v.Verdict)
	}
}

func TestBuildEvidenceCapsSiblings(t *testing.T) {
	run := model.Run{ID: 1, HeadSHA: "sha", HeadBranch: "main", Name: "CI", Event: "push", Attempt: 1}
	f := fakeProber{
		allJobs: map[int64][]gh.JobResult{
			1: {{Name: "a", Conclusion: "failure"}},
			2: {{Name: "a", Conclusion: "success"}},
			3: {{Name: "a", Conclusion: "success"}},
		},
		siblings: []gh.RunSummary{
			{ID: 2, Name: "CI", Status: "completed", Conclusion: "success", Event: "push"},
			{ID: 3, Name: "CI", Status: "completed", Conclusion: "success", Event: "push"},
		},
	}
	ev, err := buildEvidence(context.Background(), f, run, flake.Config{MaxSiblings: 1, BranchWindow: 20})
	if err != nil {
		t.Fatalf("buildEvidence: %v", err)
	}
	if len(ev.Siblings) != 1 {
		t.Errorf("siblings = %d, want capped to 1", len(ev.Siblings))
	}
}

// fakeProber is an in-memory flakeProber for buildEvidence tests.
type fakeProber struct {
	allJobs      map[int64][]gh.JobResult
	attemptJobs  map[string][]gh.JobResult // key "runID/attempt"
	siblings     []gh.RunSummary
	branchRuns   []gh.RunSummary
	branchCapped bool
}

func (f fakeProber) AllJobs(_ context.Context, id int64) ([]gh.JobResult, error) {
	return f.allJobs[id], nil
}

func (f fakeProber) AttemptJobs(_ context.Context, id int64, n int) []gh.JobResult {
	return f.attemptJobs[fmt.Sprintf("%d/%d", id, n)]
}

func (f fakeProber) RunsForSHA(_ context.Context, _ string) ([]gh.RunSummary, error) {
	return f.siblings, nil
}

func (f fakeProber) BranchRuns(_ context.Context, _ string, _ int) ([]gh.RunSummary, bool, error) {
	return f.branchRuns, f.branchCapped, nil
}

func withFlakeFlags(t *testing.T) {
	t.Helper()
	resetFlakeFlags()
	t.Cleanup(resetFlakeFlags)
}

func resetFlakeFlags() {
	flakeRepo, flakePR, flakeNDJSON = "", 0, false
	flakeBranchWindow, flakeMaxSiblings = flake.DefaultBranchWindow, flake.DefaultMaxSiblings
}
