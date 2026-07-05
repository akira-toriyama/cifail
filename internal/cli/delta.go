package cli

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/delta"
	"github.com/akira-toriyama/cifail/internal/gh"
	"github.com/akira-toriyama/cifail/internal/model"
	"github.com/spf13/cobra"
)

// delta flags. Like wait/flake, this subcommand owns its own --ndjson: root's
// is a LOCAL (not persistent) flag, so a shared one would not reach the child.
var (
	deltaRepo   string
	deltaPR     int
	deltaBranch string
	deltaBudget int
	deltaNDJSON bool
)

func newDeltaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delta [run-id]",
		Short: "Report what changed between a failing run and the last green run",
		Long: "delta anchors on a RED workflow run, finds the last GREEN run of the same\n" +
			"workflow on the same branch, and reports the input delta between them in one\n" +
			"bounded JSON document: commit range (top dirs, lockfiles, workflow edits),\n" +
			"resolved action SHAs and runner image (drift no text diff can catch), and the\n" +
			"jobs that newly fail. When both runs share the same commit, commit_range is\n" +
			"omitted and the note pivots to environment drift.\n\n" +
			"Any produced report exits 0 — including degraded ones (no green run in\n" +
			"retention, expired logs); branch on the JSON fields. 1 means the target run\n" +
			"was not red / not found, 2 usage, 3 API/IO.",
		Example: "  # diff a specific red run against its last green baseline\n" +
			"  cifail delta 28083877791\n\n" +
			"  # enter from a pull request (its latest failing run)\n" +
			"  cifail delta --pr 42 --ndjson",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          runDelta,
	}
	f := cmd.Flags()
	f.StringVar(&deltaRepo, "repo", "", "target repository as owner/repo (default: the git origin of the working dir)")
	f.IntVar(&deltaPR, "pr", 0, "diff the latest failing run for this pull request number")
	f.StringVar(&deltaBranch, "branch", "", "diff the latest failing run on this branch (default: the current branch)")
	f.IntVar(&deltaBudget, "budget-bytes", delta.DefaultBudgetBytes, "hard byte budget for the report's variable-length lists")
	f.BoolVar(&deltaNDJSON, "ndjson", false, "emit compact single-line JSON instead of pretty JSON")
	return cmd
}

func runDelta(cmd *cobra.Command, args []string) error {
	// Entry grammar: at most one of a positional run-id, --pr, or --branch;
	// none at all falls back to the current branch (root's behaviour).
	selectors := 0
	if len(args) == 1 {
		selectors++
	}
	if deltaPR != 0 {
		selectors++
	}
	if deltaBranch != "" {
		selectors++
	}
	if selectors > 1 {
		return core.Usagef("pass only one of a run-id, --pr, or --branch")
	}
	if deltaBudget <= 0 {
		return core.Usagef("--budget-bytes must be positive, got %d", deltaBudget)
	}
	var target gh.Target
	switch {
	case len(args) == 1:
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil || id <= 0 {
			return core.Usagef("run-id must be a positive integer, got %q", args[0])
		}
		target = gh.Target{RunID: id}
	case deltaPR != 0:
		target = gh.Target{PR: deltaPR}
	case deltaBranch != "":
		target = gh.Target{Branch: deltaBranch}
	}

	ctx := cmd.Context()
	dir, err := os.Getwd()
	if err != nil {
		return core.APIf("getwd: %v", err)
	}
	if target == (gh.Target{}) {
		branch, err := gh.CurrentBranch(ctx, dir)
		if err != nil {
			return interruptOr(ctx, err)
		}
		target = gh.Target{Branch: branch}
	}
	owner, repo, err := gh.ResolveRepo(ctx, deltaRepo, dir)
	if err != nil {
		return interruptOr(ctx, err)
	}
	client, err := gh.NewClient(ctx, owner, repo)
	if err != nil {
		return interruptOr(ctx, err)
	}
	// ResolveRun returns the target's newest FAILING run; a non-failing /
	// absent run is a NoFailuref (soft miss, exit 1) — delta only diffs red runs.
	run, err := client.ResolveRun(ctx, target)
	if err != nil {
		return interruptOr(ctx, err)
	}

	cfg := delta.Default()
	cfg.BudgetBytes = deltaBudget

	ev, err := buildDeltaEvidence(ctx, deltaGH{client}, run, time.Now())
	if err != nil {
		return interruptOr(ctx, err)
	}
	r := delta.Build(ev, cfg)

	if deltaNDJSON {
		printCompact(r)
	} else {
		printPretty(r)
	}
	// A produced report is a successful output — always exit 0; the agent
	// branches on last_green / same_commit / note.
	return nil
}

// logArchive is the slice of gh.LogArchive the gatherer needs, as an interface
// so tests can fake step logs (LogArchive has no exported constructor).
type logArchive interface {
	StepLog(jobName string, stepNumber int) (string, bool)
	JobLog(jobName string) (string, bool)
}

// deltaProber is the slice of gh.Client that buildDeltaEvidence needs;
// injecting it keeps the fan-out unit-testable with a fake.
type deltaProber interface {
	AllJobs(ctx context.Context, runID int64) ([]gh.JobResult, error)
	LastGreenRun(ctx context.Context, workflowID int64, branch string) (gh.RunSummary, bool, error)
	CompareCommits(ctx context.Context, base, head string) (gh.Comparison, error)
	FetchLogs(ctx context.Context, runID int64) (logArchive, error)
}

// deltaGH adapts *gh.Client to deltaProber: only FetchLogs needs wrapping (its
// concrete *gh.LogArchive return must become the logArchive interface).
type deltaGH struct {
	*gh.Client
}

func (d deltaGH) FetchLogs(ctx context.Context, runID int64) (logArchive, error) {
	a, err := d.Client.FetchLogs(ctx, runID)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// buildDeltaEvidence gathers everything delta.Build reasons over: the failing
// run's jobs, the last green baseline (plus its jobs), the commit comparison,
// and both runs' 'Set up job' logs. All IO lives here; the decision stays pure.
// Compare and log fetches are best-effort (force-pushed shas and expired log
// retention are legitimate), with the degrade reason recorded for the report.
func buildDeltaEvidence(ctx context.Context, p deltaProber, run model.Run, now time.Time) (delta.Evidence, error) {
	ev := delta.Evidence{
		Failing: delta.RunMeta{ID: run.ID, SHA: run.HeadSHA, Attempt: run.Attempt, URL: run.HTMLURL},
		Now:     now,
	}

	failingJobs, err := p.AllJobs(ctx, run.ID)
	if err != nil {
		return delta.Evidence{}, err
	}
	ev.FailingJobs = toJobOutcomes(failingJobs)

	// Without a workflow id or branch there is nowhere to hunt for a baseline;
	// Build reports the no-green degrade.
	if run.WorkflowID == 0 || run.HeadBranch == "" {
		return ev, ctx.Err()
	}
	green, ok, err := p.LastGreenRun(ctx, run.WorkflowID, run.HeadBranch)
	if err != nil {
		return delta.Evidence{}, err
	}
	if !ok {
		return ev, ctx.Err()
	}
	ev.Green = &delta.RunMeta{ID: green.ID, SHA: green.HeadSHA, URL: green.HTMLURL, StartedAt: green.StartedAt}

	greenJobs, err := p.AllJobs(ctx, green.ID)
	if err != nil {
		return delta.Evidence{}, err
	}
	ev.GreenJobs = toJobOutcomes(greenJobs)

	if green.HeadSHA != run.HeadSHA {
		cmp, err := p.CompareCommits(ctx, green.HeadSHA, run.HeadSHA)
		if err != nil {
			// A force-push can make the green sha unreachable (404); the rest
			// of the report is still worth producing.
			ev.CompareNote = "commit comparison unavailable: " + err.Error()
		} else {
			c := delta.Comparison{AheadBy: cmp.AheadBy, BehindBy: cmp.BehindBy, Capped: cmp.Capped}
			for _, f := range cmp.Files {
				c.Files = append(c.Files, delta.FileChange{
					Path: f.Path, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch,
				})
			}
			ev.Compare = &c
		}
	}

	fArchive, fErr := p.FetchLogs(ctx, run.ID)
	gArchive, gErr := p.FetchLogs(ctx, green.ID)
	if fErr != nil || gErr != nil {
		// Log retention outlives runs; an expired archive is a degrade, not an
		// error — but never compare against a half-fetched environment.
		ev.EnvNote = "environment drift unavailable: a run's log archive is missing or expired"
	} else {
		ev.FailingSetup = setupLogs(fArchive, failingJobs)
		ev.GreenSetup = setupLogs(gArchive, greenJobs)
	}

	// Best-effort fetches swallow cancellation; surface a Ctrl-C that landed
	// mid-gather as a silent 130 rather than a bogus report at exit 0.
	if ctx.Err() != nil {
		return delta.Evidence{}, ctx.Err()
	}
	return ev, nil
}

// setupLogs pulls each job's 'Set up job' text: the per-step file (step 1 by
// convention) when the archive has one, else the whole-job log whose head
// contains the same block.
func setupLogs(a logArchive, jobs []gh.JobResult) []delta.SetupLog {
	out := make([]delta.SetupLog, 0, len(jobs))
	for _, j := range jobs {
		if txt, ok := a.StepLog(j.Name, 1); ok {
			out = append(out, delta.SetupLog{Job: j.Name, Text: txt})
			continue
		}
		if txt, ok := a.JobLog(j.Name); ok {
			out = append(out, delta.SetupLog{Job: j.Name, Text: txt})
		}
	}
	return out
}

func toJobOutcomes(jobs []gh.JobResult) []delta.JobOutcome {
	out := make([]delta.JobOutcome, len(jobs))
	for i, j := range jobs {
		out[i] = delta.JobOutcome{Name: j.Name, Conclusion: j.Conclusion}
	}
	return out
}
