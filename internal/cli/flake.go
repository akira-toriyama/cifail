package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/flake"
	"github.com/akira-toriyama/cifail/internal/gh"
	"github.com/akira-toriyama/cifail/internal/model"
	"github.com/spf13/cobra"
)

// flake flags. Like wait/version, this subcommand owns its own --ndjson: root's
// is a LOCAL (not persistent) flag, so a shared one would not reach the child.
var (
	flakeRepo         string
	flakePR           int
	flakeNDJSON       bool
	flakeBranchWindow int
	flakeMaxSiblings  int
)

func newFlakeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flake [run-id]",
		Short: "Judge whether a red run is likely flaky (rerun) or needs debugging",
		Long: "flake looks at a RED workflow run and returns a 1-shot, evidence-tiered\n" +
			"verdict for the rerun-or-debug decision. It calls a run likely_flaky ONLY when\n" +
			"a failing job diverged on the same commit — it passed on a prior attempt of the\n" +
			"run, or on a sibling run of the same head sha (same workflow + event). Absent\n" +
			"that proof the verdict is insufficient_evidence, and the branch base rate is\n" +
			"reported for calibration but never escalates the verdict.\n\n" +
			"Both verdicts exit 0 — branch on the JSON `verdict` field. 1 means the target\n" +
			"run was not red / not found, 2 usage, 3 API/IO.",
		Example: "  # judge a specific red run\n" +
			"  cifail flake 28083877791\n\n" +
			"  # enter from a pull request (its latest failing run)\n" +
			"  cifail flake --pr 42 --ndjson",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          runFlake,
	}
	f := cmd.Flags()
	f.StringVar(&flakeRepo, "repo", "", "target repository as owner/repo (default: the git origin of the working dir)")
	f.IntVar(&flakePR, "pr", 0, "judge the latest failing run for this pull request number (instead of a run-id)")
	f.IntVar(&flakeBranchWindow, "branch-window", flake.DefaultBranchWindow, "how many recent branch runs to sample for the base rate")
	f.IntVar(&flakeMaxSiblings, "max-siblings", flake.DefaultMaxSiblings, "cap on same-sha sibling runs inspected")
	f.BoolVar(&flakeNDJSON, "ndjson", false, "emit compact single-line JSON instead of pretty JSON")
	return cmd
}

func runFlake(cmd *cobra.Command, args []string) error {
	// Entry grammar: exactly one of a positional <run-id> or --pr.
	hasRunID, hasPR := len(args) == 1, flakePR != 0
	switch {
	case hasRunID && hasPR:
		return core.Usagef("pass either a run-id or --pr, not both")
	case !hasRunID && !hasPR:
		return core.Usagef("pass a run-id (cifail flake <run-id>) or --pr <n>")
	}
	if flakeBranchWindow <= 0 {
		return core.Usagef("--branch-window must be positive, got %d", flakeBranchWindow)
	}
	if flakeMaxSiblings <= 0 {
		return core.Usagef("--max-siblings must be positive, got %d", flakeMaxSiblings)
	}
	var target gh.Target
	if hasRunID {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil || id <= 0 {
			return core.Usagef("run-id must be a positive integer, got %q", args[0])
		}
		target = gh.Target{RunID: id}
	} else {
		target = gh.Target{PR: flakePR}
	}

	ctx := cmd.Context()
	dir, err := os.Getwd()
	if err != nil {
		return core.APIf("getwd: %v", err)
	}
	owner, repo, err := gh.ResolveRepo(ctx, flakeRepo, dir)
	if err != nil {
		return interruptOr(ctx, err)
	}
	client, err := gh.NewClient(ctx, owner, repo)
	if err != nil {
		return interruptOr(ctx, err)
	}
	// ResolveRun returns the target's newest FAILING run; a non-failing / absent
	// run is a NoFailuref (soft miss, exit 1) — flake only judges red runs.
	run, err := client.ResolveRun(ctx, target)
	if err != nil {
		return interruptOr(ctx, err)
	}

	cfg := flake.Default()
	cfg.MaxSiblings = flakeMaxSiblings
	cfg.BranchWindow = flakeBranchWindow

	ev, err := buildEvidence(ctx, client, run, cfg)
	if err != nil {
		return interruptOr(ctx, err)
	}
	v := flake.Decide(ev)

	if flakeNDJSON {
		printCompact(v)
	} else {
		printPretty(v)
	}
	// Both verdicts exit 0; this guards only a typo'd verdict string (an internal
	// bug), mirroring wait — the JSON is already on stdout so keep it silent.
	if code := flake.ExitCode(v); code != core.CodeOK {
		return &core.Error{Code: code, Silent: true}
	}
	return nil
}

// flakeProber is the slice of gh.Client that buildEvidence needs; injecting it
// keeps the fan-out unit-testable with a fake (gh.Client satisfies it as-is).
type flakeProber interface {
	AllJobs(ctx context.Context, runID int64) ([]gh.JobResult, error)
	AttemptJobs(ctx context.Context, runID int64, attempt int) []gh.JobResult
	RunsForSHA(ctx context.Context, sha string) ([]gh.RunSummary, error)
	BranchRuns(ctx context.Context, branch string, perPage int) ([]gh.RunSummary, bool, error)
}

// buildEvidence gathers the run history flake.Decide reasons over: the target's
// jobs, its prior attempts, the same-sha siblings (capped), and the branch base
// rate. All IO lives here; the decision stays pure.
func buildEvidence(ctx context.Context, p flakeProber, run model.Run, cfg flake.Config) (flake.Evidence, error) {
	ev := flake.Evidence{
		RunID: run.ID, HeadSHA: run.HeadSHA, HeadBranch: run.HeadBranch,
		Workflow: run.Name, Event: run.Event, RunAttempt: run.Attempt, HTMLURL: run.HTMLURL,
	}

	// Target jobs (latest attempt) — the failing set + the passing baseline.
	target, err := p.AllJobs(ctx, run.ID)
	if err != nil {
		return flake.Evidence{}, err
	}
	ev.TargetJobs = toJobConclusions(target)

	// Prior attempts 1..RunAttempt-1 (best-effort: a never-rerun run has none).
	for n := 1; n < run.Attempt; n++ {
		jobs := p.AttemptJobs(ctx, run.ID, n)
		if jobs == nil {
			continue
		}
		ev.PriorAttempts = append(ev.PriorAttempts, flake.Attempt{Number: n, Jobs: toJobConclusions(jobs)})
	}

	// Same-sha siblings (completed only, target excluded, fan-out capped).
	siblings, err := p.RunsForSHA(ctx, run.HeadSHA)
	if err != nil {
		return flake.Evidence{}, err
	}
	for _, s := range siblings {
		if s.ID == run.ID || s.Status != "completed" {
			continue
		}
		if len(ev.Siblings) >= cfg.MaxSiblings {
			break
		}
		jobs, err := p.AllJobs(ctx, s.ID)
		if err != nil {
			return flake.Evidence{}, err
		}
		ev.Siblings = append(ev.Siblings, flake.SiblingRun{
			ID: s.ID, Workflow: s.Name, Event: s.Event, Conclusion: s.Conclusion,
			HTMLURL: s.HTMLURL, Jobs: toJobConclusions(jobs),
		})
	}

	// Branch base rate over completed runs in the window (informational tier).
	if run.HeadBranch != "" {
		runs, capped, err := p.BranchRuns(ctx, run.HeadBranch, cfg.BranchWindow)
		if err != nil {
			return flake.Evidence{}, err
		}
		completed, failures := 0, 0
		for _, r := range runs {
			if r.Status != "completed" {
				continue
			}
			completed++
			if r.Conclusion == "failure" {
				failures++
			}
		}
		ev.Branch = flake.BranchHistory{
			Branch: run.HeadBranch, Runs: completed, Failures: failures,
			Window: fmt.Sprintf("last %d runs on %s", completed, run.HeadBranch), Capped: capped,
		}
	}

	// The best-effort attempt fetches swallow cancellation; surface a Ctrl-C that
	// landed mid-gather as a silent 130 rather than a bogus verdict at exit 0.
	if ctx.Err() != nil {
		return flake.Evidence{}, ctx.Err()
	}
	return ev, nil
}

func toJobConclusions(jobs []gh.JobResult) []flake.JobConclusion {
	out := make([]flake.JobConclusion, len(jobs))
	for i, j := range jobs {
		out[i] = flake.JobConclusion{Name: j.Name, Conclusion: j.Conclusion, HTMLURL: j.HTMLURL}
	}
	return out
}
