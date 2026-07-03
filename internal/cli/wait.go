package cli

import (
	"os"
	"time"

	"github.com/akira-toriyama/cifail/internal/collect"
	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/extract"
	"github.com/akira-toriyama/cifail/internal/gh"
	"github.com/akira-toriyama/cifail/internal/model"
	"github.com/akira-toriyama/cifail/internal/wait"
	"github.com/spf13/cobra"
)

var (
	waitRepo     string
	waitSHA      string
	waitTimeout  time.Duration
	waitInterval time.Duration
	waitBudget   int
	waitContext  int
	waitNDJSON   bool
)

func newWaitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wait",
		Short: "Block until the current commit's CI runs finish, then print a verdict",
		Long: "wait resolves the workflow runs of a commit (HEAD by default), blocks until\n" +
			"they finish, and prints a worst-of verdict — embedding each failing step's\n" +
			"budgeted excerpts when red. Exit: 0 green, 1 red, 124 not-concluded (the JSON\n" +
			"conclusion says pending=re-run or timed_out), 2 usage, 3 API/IO.\n\n" +
			"A single call blocks at most ~9m (under the agent shell's limit); if CI is\n" +
			"still running it prints conclusion=pending and exits 124 — re-run the same\n" +
			"command to resume (elapsed is measured from the run's start, so it stays\n" +
			"accurate across re-runs).",
		Example: "  # push, then block until CI concludes\n" +
			"  git push && cifail wait\n\n" +
			"  # a longer deadline and a tighter budget, compact JSON\n" +
			"  cifail wait --timeout 20m --budget-bytes 4096 --ndjson",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          runWait,
	}
	f := cmd.Flags()
	f.StringVar(&waitRepo, "repo", "", "target repository as owner/repo (default: the git origin of the working dir)")
	f.StringVar(&waitSHA, "sha", "", "full commit sha to wait on (default: HEAD of the working dir)")
	f.DurationVar(&waitTimeout, "timeout", wait.DefaultTimeout, "overall deadline for the runs to finish")
	f.DurationVar(&waitInterval, "interval", wait.DefaultInterval, "how often to poll run status")
	f.IntVar(&waitBudget, "budget-bytes", extract.Default().BudgetBytes, "byte budget for the kept log excerpts (per failing run)")
	f.IntVar(&waitContext, "context", extract.Default().Context, "lines of context kept around each matched error line")
	f.BoolVar(&waitNDJSON, "ndjson", false, "emit compact single-line JSON instead of pretty JSON")
	return cmd
}

func runWait(cmd *cobra.Command, args []string) error {
	if waitBudget <= 0 {
		return core.Usagef("--budget-bytes must be positive, got %d", waitBudget)
	}
	if waitContext < 0 {
		return core.Usagef("--context must be >= 0, got %d", waitContext)
	}
	if waitTimeout <= 0 {
		return core.Usagef("--timeout must be positive, got %s", waitTimeout)
	}
	if waitInterval <= 0 {
		return core.Usagef("--interval must be positive, got %s", waitInterval)
	}
	// GitHub's head_sha run filter matches only the full 40-char sha; a short one
	// silently returns zero runs, which would read as a false green (no_runs).
	if waitSHA != "" && !isFullSHA(waitSHA) {
		return core.Usagef("--sha must be a full 40-character commit sha, got %q (omit --sha to use HEAD)", waitSHA)
	}

	dir, err := os.Getwd()
	if err != nil {
		return core.APIf("getwd: %v", err)
	}
	owner, repo, err := gh.ResolveRepo(waitRepo, dir)
	if err != nil {
		return err
	}
	sha := waitSHA
	if sha == "" {
		if sha, err = gh.CurrentSHA(dir); err != nil {
			return err
		}
	}
	client, err := gh.NewClient(owner, repo)
	if err != nil {
		return err
	}
	cfg := extract.Default()
	cfg.BudgetBytes = waitBudget
	cfg.Context = waitContext

	v, err := wait.Run(waitPoller{client, cfg}, realClock{}, wait.Options{
		SHA: sha, Timeout: waitTimeout, Interval: waitInterval,
		MaxBlock: wait.DefaultMaxBlock, StartupGrace: wait.DefaultStartupGrace,
	})
	if err != nil {
		return err
	}

	if waitNDJSON {
		printCompact(v)
	} else {
		printPretty(v)
	}
	if code := wait.ExitCode(v); code != core.CodeOK {
		return &core.Error{Code: code, Silent: true}
	}
	return nil
}

// waitPoller adapts the gh client + extract config to wait.Poller.
type waitPoller struct {
	c   *gh.Client
	cfg extract.Config
}

func (p waitPoller) RunsForSHA(sha string) ([]wait.RunState, error) {
	runs, err := p.c.RunsForSHA(sha)
	if err != nil {
		return nil, err
	}
	states := make([]wait.RunState, len(runs))
	for i, r := range runs {
		states[i] = wait.RunState{ID: r.ID, Name: r.Name, Status: r.Status,
			Conclusion: r.Conclusion, Event: r.Event, HTMLURL: r.HTMLURL, StartedAt: r.StartedAt}
	}
	return states, nil
}

func (p waitPoller) Excerpts(runID int64) (*model.Result, error) {
	return collect.Collect(p.c, gh.Target{RunID: runID}, p.cfg)
}

// isFullSHA reports whether s is a full 40-character hexadecimal commit sha.
func isFullSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// realClock is the production Clock.
type realClock struct{}

func (realClock) Now() time.Time        { return time.Now() }
func (realClock) Sleep(d time.Duration) { time.Sleep(d) }
