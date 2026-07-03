// Package cli is cifail's cobra adapter: it parses flags, drives the collect
// pipeline, and renders the result (pretty JSON by default, compact with
// --ndjson) — mapping everything to cifail's exit-code contract. It holds no
// extraction logic; that lives in internal/{gh,collect,extract}.
package cli

import (
	"fmt"
	"os"

	"github.com/akira-toriyama/cifail/internal/collect"
	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/extract"
	"github.com/akira-toriyama/cifail/internal/gh"
	"github.com/akira-toriyama/cifail/internal/version"
	"github.com/spf13/cobra"
)

// run flags
var (
	flagRepo    string
	flagPR      int
	flagBranch  string
	flagRun     int64
	flagBudget  int
	flagContext int
	flagNDJSON  bool
)

// Execute builds the root command, runs it, and maps the result to cifail's
// exit-code contract:
//
//	0 extracted / 1 no failing run / 2 usage|bad input / 3 API|IO
//
// On a non-zero exit it prints {"error":{...}} to stderr, keeping stdout pure.
func Execute() int {
	root := newRootCmd()
	err := root.Execute()
	if err == nil {
		return int(core.CodeOK)
	}
	ce := core.AsError(err)
	if ce == nil {
		// A bare error here is a cobra parse/usage problem, which is a usage
		// error by contract.
		ce = &core.Error{Code: core.CodeUsage, Msg: err.Error()}
	}
	renderError(ce)
	return int(ce.Code)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "cifail",
		Short: "Extract the failing logs of a GitHub Actions run in a bounded, high-signal form",
		Long: "cifail resolves a PR/branch/run to its latest FAILING workflow run, downloads\n" +
			"the run log archive once, keeps only the failed steps, and pares them to the\n" +
			"error lines (plus context) that fit a byte budget — emitting JSON for an agent\n" +
			"instead of the ~130 KB `gh run view --log-failed` dump.\n\n" +
			"With no target flags it inspects the current branch of the repo in the working\n" +
			"directory, reusing your `gh` CLI authentication.",
		Example: "  # the latest failing run for the current branch\n" +
			"  cifail\n\n" +
			"  # a specific PR, with a tighter budget\n" +
			"  cifail --pr 42 --budget-bytes 4096\n\n" +
			"  # a specific run in another repo, compact one-line JSON\n" +
			"  cifail --repo akira-toriyama/chord --run 28083877791 --ndjson",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Resolve().String(),
		RunE:          runExtract,
	}
	root.SetVersionTemplate("cifail {{.Version}}\n")

	f := root.Flags()
	f.StringVar(&flagRepo, "repo", "", "target repository as owner/repo (default: the git origin of the working dir)")
	f.IntVar(&flagPR, "pr", 0, "inspect the latest failing run for this pull request number")
	f.StringVar(&flagBranch, "branch", "", "inspect the latest failing run for this branch (default: the current branch)")
	f.Int64Var(&flagRun, "run", 0, "inspect this specific workflow run id")
	f.IntVar(&flagBudget, "budget-bytes", extract.Default().BudgetBytes, "byte budget for the kept log excerpts")
	f.IntVar(&flagContext, "context", extract.Default().Context, "lines of context kept around each matched error line")
	f.BoolVar(&flagNDJSON, "ndjson", false, "emit compact single-line JSON instead of pretty JSON")

	root.AddCommand(newVersionCmd())
	return root
}

// runExtract is the root command: resolve the target, collect, render.
func runExtract(cmd *cobra.Command, args []string) error {
	if flagBudget <= 0 {
		return core.Usagef("--budget-bytes must be positive, got %d", flagBudget)
	}
	if flagContext < 0 {
		return core.Usagef("--context must be >= 0, got %d", flagContext)
	}

	dir, err := os.Getwd()
	if err != nil {
		return core.APIf("getwd: %v", err)
	}

	owner, repo, err := gh.ResolveRepo(flagRepo, dir)
	if err != nil {
		return err
	}

	target, err := resolveTarget(dir)
	if err != nil {
		return err
	}

	client, err := gh.NewClient(owner, repo)
	if err != nil {
		return err
	}

	cfg := extract.Default()
	cfg.BudgetBytes = flagBudget
	cfg.Context = flagContext

	result, err := collect.Collect(client, target, cfg)
	if err != nil {
		return err
	}
	return renderResult(result)
}

// resolveTarget picks the run target from the flags, defaulting to the current
// branch when none of --run/--pr/--branch is set.
func resolveTarget(dir string) (gh.Target, error) {
	switch {
	case flagRun != 0:
		return gh.Target{RunID: flagRun}, nil
	case flagPR != 0:
		return gh.Target{PR: flagPR}, nil
	case flagBranch != "":
		return gh.Target{Branch: flagBranch}, nil
	default:
		branch, err := gh.CurrentBranch(dir)
		if err != nil {
			return gh.Target{}, err
		}
		return gh.Target{Branch: branch}, nil
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the cifail version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			info := version.Resolve()
			if flagNDJSON {
				printCompact(info)
				return nil
			}
			fmt.Fprintf(out, "cifail %s\n", info.String())
			return nil
		},
	}
}
