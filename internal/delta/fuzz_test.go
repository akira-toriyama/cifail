package delta

import (
	"fmt"
	"testing"
	"time"
)

// boundedInt maps an arbitrary fuzzed int into [0, max] without overflow.
func boundedInt(n, max int) int {
	if n < 0 {
		n = -(n + 1) // avoids math.MinInt negation overflow
	}
	if max <= 0 {
		return 0
	}
	return n % (max + 1)
}

// FuzzBuild guards the pure invariants: never panic, budget never exceeded,
// same_commit iff the shas match, and the always-present arrays stay non-nil.
func FuzzBuild(f *testing.F) {
	f.Add("abc", "abc", 4096, 3, 2, 2, true)
	f.Add("abc", "def", 1, 5, 3, 4, true)
	f.Add("", "", 64, 0, 0, 0, false)
	f.Fuzz(func(t *testing.T, fsha, gsha string, budgetBytes, nFiles, nActions, nJobs int, hasGreen bool) {
		nFiles, nActions, nJobs = boundedInt(nFiles, 40), boundedInt(nActions, 10), boundedInt(nJobs, 20)
		cfg := Config{BudgetBytes: boundedInt(budgetBytes, 1<<16) + 1}

		ev := Evidence{
			Failing: RunMeta{ID: 1, SHA: fsha},
			Now:     time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC),
		}
		if hasGreen {
			ev.Green = &RunMeta{ID: 2, SHA: gsha}
			files := make([]FileChange, nFiles)
			for i := range files {
				files[i] = FileChange{Path: fmt.Sprintf("dir%d/sub/file.go", i), Additions: i}
			}
			ev.Compare = &Comparison{AheadBy: nFiles, Files: files}
			for i := 0; i < nActions; i++ {
				txt := fmt.Sprintf("2026-07-01T00:00:00Z Download action repository 'o/r%d@v1' (SHA:aa%d)\n", i, i)
				ev.FailingSetup = append(ev.FailingSetup, SetupLog{Job: "j", Text: txt})
				ev.GreenSetup = append(ev.GreenSetup, SetupLog{Job: "j", Text: txt})
			}
			for i := 0; i < nJobs; i++ {
				ev.FailingJobs = append(ev.FailingJobs, JobOutcome{Name: fmt.Sprintf("job%d", i), Conclusion: "failure"})
				ev.GreenJobs = append(ev.GreenJobs, JobOutcome{Name: fmt.Sprintf("job%d", i), Conclusion: "success"})
			}
		}

		r := Build(ev, cfg)

		if r.Budget.UsedBytes > cfg.BudgetBytes {
			t.Fatalf("UsedBytes %d > limit %d", r.Budget.UsedBytes, cfg.BudgetBytes)
		}
		if r.Budget.OmittedItems < 0 {
			t.Fatalf("OmittedItems = %d", r.Budget.OmittedItems)
		}
		if r.Jobs.NewlyFailing == nil {
			t.Fatal("NewlyFailing is nil, must render []")
		}
		if hasGreen {
			if r.SameCommit != (fsha == gsha) {
				t.Fatalf("SameCommit = %v for fsha=%q gsha=%q", r.SameCommit, fsha, gsha)
			}
			if r.SameCommit && r.CommitRange != nil {
				t.Fatal("CommitRange present on same commit")
			}
		} else if r.LastGreen != nil {
			t.Fatal("LastGreen set without green evidence")
		}
	})
}
