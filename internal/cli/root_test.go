package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/extract"
	"github.com/akira-toriyama/cifail/internal/gh"
)

func withRootFlags(t *testing.T) {
	t.Helper()
	resetRootFlags()
	t.Cleanup(resetRootFlags)
}

func resetRootFlags() {
	flagRun, flagPR, flagBranch = 0, 0, ""
	flagBudget, flagContext = extract.Default().BudgetBytes, extract.Default().Context
}

// resolveTarget honors --run over --pr over --branch; only with none set does it
// fall back to the current branch (which needs IO and is not exercised here).
func TestResolveTargetPrecedence(t *testing.T) {
	withRootFlags(t)
	cases := []struct {
		name   string
		run    int64
		pr     int
		branch string
		want   gh.Target
	}{
		{"run only", 7, 0, "", gh.Target{RunID: 7}},
		{"pr only", 0, 42, "", gh.Target{PR: 42}},
		{"branch only", 0, 0, "dev", gh.Target{Branch: "dev"}},
		{"run wins over pr and branch", 7, 42, "dev", gh.Target{RunID: 7}},
		{"pr wins over branch", 0, 42, "dev", gh.Target{PR: 42}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flagRun, flagPR, flagBranch = tc.run, tc.pr, tc.branch
			got, err := resolveTarget(context.Background(), "")
			if err != nil {
				t.Fatalf("resolveTarget: %v", err)
			}
			if got != tc.want {
				t.Errorf("resolveTarget = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// runExtract rejects an invalid budget/context before any network work — a usage
// error (exit 2), mirroring the other subcommands' flag guards.
func TestRunExtractRejectsBadFlags(t *testing.T) {
	withRootFlags(t)
	cases := []struct {
		name            string
		budget, context int
	}{
		{"zero budget", 0, 3},
		{"negative budget", -1, 3},
		{"negative context", 8192, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flagBudget, flagContext = tc.budget, tc.context
			err := runExtract(nil, nil)
			var ce *core.Error
			if !errors.As(err, &ce) || ce.Code != core.CodeUsage {
				t.Fatalf("want usage error, got %v", err)
			}
		})
	}
}
