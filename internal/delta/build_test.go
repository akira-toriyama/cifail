package delta

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func greenMeta(sha string, started time.Time) *RunMeta {
	return &RunMeta{ID: 120, SHA: sha, URL: "g", StartedAt: started}
}

var now = time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

// The flagship degrade: no green baseline still produces a report (exit 0)
// with an explicit null last_green and a note — never an error.
func TestBuildNoGreen(t *testing.T) {
	r := Build(Evidence{
		Failing: RunMeta{ID: 123, SHA: "abc", Attempt: 2, URL: "f"},
		Now:     now,
	}, Default())
	if r.LastGreen != nil {
		t.Errorf("LastGreen = %+v, want nil", r.LastGreen)
	}
	if !strings.Contains(r.Note, "no green run") {
		t.Errorf("Note = %q, want a no-green explanation", r.Note)
	}
	if r.CommitRange != nil || r.Environment != nil {
		t.Error("CommitRange/Environment should be absent with no baseline")
	}
	if r.Jobs.NewlyFailing == nil {
		t.Error("NewlyFailing must be non-nil ([])")
	}
}

// Zero-commit pivot: same sha means git has no answer — commit_range is
// omitted even if a Comparison was (wrongly) supplied, and the note points at
// environment drift.
func TestBuildSameCommitPivots(t *testing.T) {
	r := Build(Evidence{
		Failing: RunMeta{ID: 123, SHA: "abc"},
		Green:   greenMeta("abc", now.Add(-49*time.Hour)),
		Now:     now,
		Compare: &Comparison{AheadBy: 1},
	}, Default())
	if !r.SameCommit {
		t.Fatal("SameCommit = false, want true")
	}
	if r.CommitRange != nil {
		t.Error("CommitRange present on same commit, want omitted")
	}
	if !strings.Contains(r.Note, "environment") {
		t.Errorf("Note = %q, want a pivot to environment drift", r.Note)
	}
	if r.LastGreen == nil || r.LastGreen.AgeDays != 2 {
		t.Errorf("LastGreen = %+v, want age_days 2 (49h)", r.LastGreen)
	}
}

// Action drift: same ref, different resolved SHA across the runs. A ref seen
// on only one side must never count as drifted.
func TestBuildEnvironmentDrift(t *testing.T) {
	green := []SetupLog{{Job: "test", Text: "2026-07-01T00:00:00Z Download action repository 'actions/checkout@v4' (SHA:aaa1)\n2026-07-01T00:00:00Z Image: ubuntu-24.04\n2026-07-01T00:00:00Z Version: 20250601.1.0"}}
	failing := []SetupLog{{Job: "test", Text: "2026-07-02T00:00:00Z Download action repository 'actions/checkout@v4' (SHA:bbb2)\n2026-07-02T00:00:00Z Download action repository 'actions/cache@v4' (SHA:ccc3)\n2026-07-02T00:00:00Z Image: ubuntu-24.04\n2026-07-02T00:00:00Z Version: 20250620.2.0"}}
	r := Build(Evidence{
		Failing: RunMeta{ID: 123, SHA: "abc"}, Green: greenMeta("abc", now), Now: now,
		FailingSetup: failing, GreenSetup: green,
	}, Default())
	if r.Environment == nil {
		t.Fatal("Environment = nil, want drift report")
	}
	var checkout, cache int = -1, -1
	for i, a := range r.Environment.Actions {
		switch a.Ref {
		case "actions/checkout@v4":
			checkout = i
		case "actions/cache@v4":
			cache = i
		}
	}
	if checkout < 0 || !r.Environment.Actions[checkout].Drifted {
		t.Errorf("checkout@v4 should be drifted: %+v", r.Environment.Actions)
	}
	if cache < 0 || r.Environment.Actions[cache].Drifted {
		t.Errorf("cache@v4 (one side only) must not be drifted: %+v", r.Environment.Actions)
	}
	if r.Environment.Runner == nil || !r.Environment.Runner.Drifted {
		t.Errorf("Runner = %+v, want drifted image versions", r.Environment.Runner)
	}
}

// Expired logs degrade environment to absent, with the gatherer's note carried
// through — never an error, never a fabricated comparison.
func TestBuildEnvNoteOnMissingLogs(t *testing.T) {
	r := Build(Evidence{
		Failing: RunMeta{ID: 1, SHA: "a"}, Green: greenMeta("b", now), Now: now,
		EnvNote: "environment drift unavailable: log archive missing or expired",
	}, Default())
	if r.Environment != nil {
		t.Error("Environment should be absent without both setup logs")
	}
	if !strings.Contains(r.Note, "environment drift unavailable") {
		t.Errorf("Note = %q, want the env degrade note", r.Note)
	}
}

func TestBuildNewlyFailing(t *testing.T) {
	r := Build(Evidence{
		Failing: RunMeta{ID: 1, SHA: "a"}, Green: greenMeta("b", now), Now: now,
		FailingJobs: []JobOutcome{
			{Name: "test (1.26)", Conclusion: "failure"},
			{Name: "lint", Conclusion: "failure"},
			{Name: "build", Conclusion: "success"},
		},
		GreenJobs: []JobOutcome{
			{Name: "test (1.26)", Conclusion: "success"},
			{Name: "lint", Conclusion: "skipped"}, // not a pass -> not "newly" failing
		},
	}, Default())
	want := []string{"test (1.26)"}
	if !reflect.DeepEqual(r.Jobs.NewlyFailing, want) {
		t.Errorf("NewlyFailing = %v, want %v", r.Jobs.NewlyFailing, want)
	}
}

// The budget is a hard cap with exact accounting: with a tiny limit every list
// empties, omitted counts every dropped item, and used stays under the limit.
func TestBuildBudgetExactAccounting(t *testing.T) {
	ev := Evidence{
		Failing: RunMeta{ID: 1, SHA: "abc"}, Green: greenMeta("def", now), Now: now,
		Compare: &Comparison{Files: []FileChange{
			{Path: "a/b/one.go"}, {Path: "c/d/two.go"}, {Path: "go.sum", Additions: 1},
		}},
		FailingJobs: []JobOutcome{{Name: "j1", Conclusion: "failure"}},
		GreenJobs:   []JobOutcome{{Name: "j1", Conclusion: "success"}},
	}
	full := Build(ev, Config{BudgetBytes: 1 << 20})
	total := len(full.CommitRange.TopDirs) + len(full.CommitRange.Lockfiles) +
		len(full.CommitRange.WorkflowChanges) + len(full.Jobs.NewlyFailing)
	if total == 0 || full.Budget.OmittedItems != 0 {
		t.Fatalf("full build: total=%d omitted=%d, want items and 0 omitted", total, full.Budget.OmittedItems)
	}
	if full.Budget.UsedBytes <= 0 || full.Budget.UsedBytes > 1<<20 {
		t.Fatalf("full build UsedBytes = %d", full.Budget.UsedBytes)
	}

	tiny := Build(ev, Config{BudgetBytes: 1})
	kept := len(tiny.CommitRange.TopDirs) + len(tiny.CommitRange.Lockfiles) +
		len(tiny.CommitRange.WorkflowChanges) + len(tiny.Jobs.NewlyFailing)
	if kept != 0 {
		t.Errorf("tiny budget kept %d items, want 0", kept)
	}
	if tiny.Budget.OmittedItems != total {
		t.Errorf("tiny budget omitted = %d, want %d", tiny.Budget.OmittedItems, total)
	}
	if tiny.Budget.UsedBytes > 1 {
		t.Errorf("UsedBytes = %d > limit 1", tiny.Budget.UsedBytes)
	}
}

// The report's total marshalled size stays sane and lists render [] not null
// even when everything is trimmed.
func TestBuildTinyBudgetStillValidJSON(t *testing.T) {
	r := Build(Evidence{
		Failing: RunMeta{ID: 1, SHA: "a"}, Green: greenMeta("b", now), Now: now,
		Compare: &Comparison{Files: []FileChange{{Path: "x/y/z.go"}}},
	}, Config{BudgetBytes: 1})
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"top_dirs":[]`) {
		t.Errorf("trimmed lists must render []: %s", b)
	}
}

// A negative budget must not break the used<=limit invariant Build documents:
// it clamps to 0 (drop everything) rather than trusting a caller to pre-validate.
func TestBuildNegativeBudgetClamped(t *testing.T) {
	r := Build(Evidence{
		Failing: RunMeta{ID: 1, SHA: "a"}, Green: greenMeta("b", now), Now: now,
		Compare: &Comparison{Files: []FileChange{{Path: "x/y/z.go"}}},
	}, Config{BudgetBytes: -5})
	if r.Budget.UsedBytes > r.Budget.LimitBytes {
		t.Errorf("UsedBytes %d > LimitBytes %d — invariant broken on negative budget", r.Budget.UsedBytes, r.Budget.LimitBytes)
	}
	if r.Budget.LimitBytes < 0 {
		t.Errorf("LimitBytes = %d, want clamped to >= 0", r.Budget.LimitBytes)
	}
}
