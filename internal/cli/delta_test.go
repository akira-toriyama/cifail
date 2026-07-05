package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/gh"
	"github.com/akira-toriyama/cifail/internal/model"
)

func withDeltaFlags(t *testing.T) {
	t.Helper()
	resetDeltaFlags()
	t.Cleanup(resetDeltaFlags)
}

func resetDeltaFlags() {
	deltaRepo, deltaPR, deltaBranch, deltaBudget, deltaNDJSON = "", 0, "", 4096, false
}

// Usage errors must be caught before ANY IO, with exit code 2.
func TestRunDeltaUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		prep func()
	}{
		{"run-id and --pr", []string{"123"}, func() { deltaPR = 7 }},
		{"run-id and --branch", []string{"123"}, func() { deltaBranch = "main" }},
		{"--pr and --branch", nil, func() { deltaPR = 7; deltaBranch = "main" }},
		{"bad run-id", []string{"nope"}, func() {}},
		{"negative run-id", []string{"-3"}, func() {}},
		{"zero budget", []string{"123"}, func() { deltaBudget = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withDeltaFlags(t)
			tc.prep()
			err := runDelta(nil, tc.args)
			var ce *core.Error
			if !errors.As(err, &ce) || ce.Code != core.CodeUsage {
				t.Fatalf("err = %v, want *core.Error with CodeUsage", err)
			}
		})
	}
}

// fakeDeltaArchive returns 'Set up job' text keyed by exact job name.
type fakeDeltaArchive map[string]string

func (a fakeDeltaArchive) StepLog(job string, n int) (string, bool) {
	if n != 1 {
		return "", false
	}
	s, ok := a[job]
	return s, ok
}

func (a fakeDeltaArchive) JobLog(job string) (string, bool) { return "", false }

type fakeDeltaProber struct {
	allJobs map[int64][]gh.JobResult
	green   gh.RunSummary
	greenOK bool
	cmp     gh.Comparison
	cmpErr  error
	logs    map[int64]fakeDeltaArchive
	logsErr map[int64]error
}

func (f *fakeDeltaProber) AllJobs(_ context.Context, runID int64) ([]gh.JobResult, error) {
	return f.allJobs[runID], nil
}

func (f *fakeDeltaProber) LastGreenRun(_ context.Context, _ int64, _ string) (gh.RunSummary, bool, error) {
	return f.green, f.greenOK, nil
}

func (f *fakeDeltaProber) CompareCommits(_ context.Context, _, _ string) (gh.Comparison, error) {
	return f.cmp, f.cmpErr
}

func (f *fakeDeltaProber) FetchLogs(_ context.Context, runID int64) (logArchive, error) {
	if err := f.logsErr[runID]; err != nil {
		return nil, err
	}
	return f.logs[runID], nil
}

var deltaTestRun = model.Run{
	ID: 123, WorkflowID: 7, HeadBranch: "main", HeadSHA: "abc",
	Conclusion: "failure", Attempt: 1, HTMLURL: "f",
}

// The full gather: green found on a different sha -> compare runs, both
// archives mined, jobs collected for both sides.
func TestBuildDeltaEvidence(t *testing.T) {
	setup := "2026-07-01T00:00:00Z Download action repository 'actions/checkout@v4' (SHA:aaa)"
	p := &fakeDeltaProber{
		allJobs: map[int64][]gh.JobResult{
			123: {{Name: "test", Conclusion: "failure"}},
			120: {{Name: "test", Conclusion: "success"}},
		},
		green:   gh.RunSummary{ID: 120, HeadSHA: "def", StartedAt: time.Now().Add(-24 * time.Hour)},
		greenOK: true,
		cmp:     gh.Comparison{AheadBy: 2, Files: []gh.ComparedFile{{Path: "go.sum", Additions: 1}}},
		logs: map[int64]fakeDeltaArchive{
			123: {"test": setup},
			120: {"test": setup},
		},
	}
	ev, err := buildDeltaEvidence(context.Background(), p, deltaTestRun, time.Now())
	if err != nil {
		t.Fatalf("buildDeltaEvidence: %v", err)
	}
	if ev.Green == nil || ev.Green.SHA != "def" {
		t.Fatalf("Green = %+v, want sha def", ev.Green)
	}
	if ev.Compare == nil || ev.Compare.AheadBy != 2 || len(ev.Compare.Files) != 1 {
		t.Fatalf("Compare = %+v, want ahead 2 with 1 file", ev.Compare)
	}
	if len(ev.FailingSetup) != 1 || len(ev.GreenSetup) != 1 {
		t.Fatalf("setups = %d/%d, want 1/1", len(ev.FailingSetup), len(ev.GreenSetup))
	}
	if len(ev.FailingJobs) != 1 || len(ev.GreenJobs) != 1 {
		t.Fatalf("jobs = %d/%d, want 1/1", len(ev.FailingJobs), len(ev.GreenJobs))
	}
}

// Same sha: the comparison must be SKIPPED (no wasted call, no fake range).
func TestBuildDeltaEvidenceSameCommitSkipsCompare(t *testing.T) {
	p := &fakeDeltaProber{
		allJobs: map[int64][]gh.JobResult{123: {}, 120: {}},
		green:   gh.RunSummary{ID: 120, HeadSHA: "abc"},
		greenOK: true,
		cmpErr:  errors.New("CompareCommits must not be called for the same sha"),
	}
	ev, err := buildDeltaEvidence(context.Background(), p, deltaTestRun, time.Now())
	if err != nil {
		t.Fatalf("buildDeltaEvidence: %v", err)
	}
	if ev.Compare != nil || ev.CompareNote != "" {
		t.Errorf("Compare = %+v note=%q, want none for same sha", ev.Compare, ev.CompareNote)
	}
}

// Expired/missing archives are a degrade with a note, never an error: old
// green runs legitimately outlive their logs.
func TestBuildDeltaEvidenceExpiredLogs(t *testing.T) {
	p := &fakeDeltaProber{
		allJobs: map[int64][]gh.JobResult{123: {}, 120: {}},
		green:   gh.RunSummary{ID: 120, HeadSHA: "def"},
		greenOK: true,
		logsErr: map[int64]error{120: core.APIf("not found: GET .../logs (404)")},
	}
	ev, err := buildDeltaEvidence(context.Background(), p, deltaTestRun, time.Now())
	if err != nil {
		t.Fatalf("buildDeltaEvidence: %v", err)
	}
	if len(ev.FailingSetup) != 0 || len(ev.GreenSetup) != 0 {
		t.Error("setups must be empty when either archive is unavailable")
	}
	if !strings.Contains(ev.EnvNote, "unavailable") {
		t.Errorf("EnvNote = %q, want an unavailability note", ev.EnvNote)
	}
}

// Both archives fetch cleanly, but neither contains a discoverable 'Set up
// job' log for the job names involved: the degrade must still carry a note,
// never a silently-omitted environment block.
func TestBuildDeltaEvidenceEmptySetupNote(t *testing.T) {
	p := &fakeDeltaProber{
		allJobs: map[int64][]gh.JobResult{
			123: {{Name: "test", Conclusion: "failure"}},
			120: {{Name: "test", Conclusion: "success"}},
		},
		green:   gh.RunSummary{ID: 120, HeadSHA: "def"},
		greenOK: true,
		logs: map[int64]fakeDeltaArchive{
			123: {"other-job": "irrelevant"},
			120: {"other-job": "irrelevant"},
		},
	}
	ev, err := buildDeltaEvidence(context.Background(), p, deltaTestRun, time.Now())
	if err != nil {
		t.Fatalf("buildDeltaEvidence: %v", err)
	}
	if len(ev.FailingSetup) != 0 || len(ev.GreenSetup) != 0 {
		t.Error("setups must be empty when no job name in the archive matches")
	}
	if !strings.Contains(ev.EnvNote, "no 'Set up job'") {
		t.Errorf("EnvNote = %q, want a no-setup-log note", ev.EnvNote)
	}
}

// A compare failure (force-push made the green sha unreachable) degrades with
// a note instead of failing a report that is still mostly producible.
func TestBuildDeltaEvidenceCompareDegrades(t *testing.T) {
	p := &fakeDeltaProber{
		allJobs: map[int64][]gh.JobResult{123: {}, 120: {}},
		green:   gh.RunSummary{ID: 120, HeadSHA: "def"},
		greenOK: true,
		cmpErr:  core.APIf("not found: GET .../compare (404)"),
	}
	ev, err := buildDeltaEvidence(context.Background(), p, deltaTestRun, time.Now())
	if err != nil {
		t.Fatalf("buildDeltaEvidence: %v", err)
	}
	if ev.Compare != nil {
		t.Errorf("Compare = %+v, want nil on degrade", ev.Compare)
	}
	if !strings.Contains(ev.CompareNote, "commit comparison unavailable") {
		t.Errorf("CompareNote = %q, want degrade note", ev.CompareNote)
	}
}
