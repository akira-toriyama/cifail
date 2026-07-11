// Package wait blocks until a commit's GitHub Actions workflow runs finish, then
// assembles a worst-of Verdict — embedding failed-step excerpts (via the existing
// extract pipeline) when red. It owns no IO: a Poller (gh-backed) and a Clock are
// injected, so the poll / timeout / resume / aggregation logic is unit-tested with
// fakes. A ctx threads through the poll loop and the injected Clock so an interrupt
// (Ctrl-C) cancels the minutes-long block promptly. Exit codes: 0 green|no_runs,
// 1 red, 124 pending|timed_out (130 interrupt is mapped by the caller).
package wait

import (
	"context"
	"strings"
	"time"

	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/model"
)

// Defaults the cli passes; tests pass their own.
const (
	DefaultTimeout      = 30 * time.Minute
	DefaultInterval     = 10 * time.Second
	DefaultMaxBlock     = 540 * time.Second // < the agent shell's 600s kill: return pending first
	DefaultStartupGrace = 60 * time.Second  // no-runs-yet window before concluding no_runs
)

// RunState is a workflow run's poll snapshot (wait-local, so wait needs no gh dep).
type RunState struct {
	ID         int64
	Name       string
	Status     string // queued | in_progress | completed
	Conclusion string
	Event      string
	HTMLURL    string
	StartedAt  time.Time
}

// Poller is the IO wait depends on, satisfied by a gh-backed adapter in cli.
type Poller interface {
	RunsForSHA(ctx context.Context, sha string) ([]RunState, error)
	Excerpts(ctx context.Context, runID int64) (*model.Result, error) // failing-run jobs + budget
}

// Clock is injected so tests neither sleep nor read the wall clock. Sleep takes a
// ctx and returns its error if cancelled mid-sleep, so a Ctrl-C interrupts the
// wait between polls instead of blocking for the full interval.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

// Options configures one wait invocation.
type Options struct {
	SHA          string
	Timeout      time.Duration // overall deadline, from the earliest run's start
	Interval     time.Duration
	MaxBlock     time.Duration // per-invocation ceiling (shell-safety); returns pending
	StartupGrace time.Duration
}

// Run polls the sha's runs until terminal, the deadline, or the per-call ceiling,
// and returns the assembled Verdict. Poller (API/IO) errors propagate unchanged.
// On a Ctrl-C it returns a cancellation error — ctx.Err() from the loop guard or
// the sleep, or the poller's own context-canceled error if the interrupt lands
// mid-poll — which the caller maps to the interrupt exit via ctx.Err().
func Run(ctx context.Context, p Poller, clk Clock, o Options) (model.Verdict, error) {
	callStart := clk.Now()
	seenRuns := false
	for {
		if err := ctx.Err(); err != nil {
			return model.Verdict{}, err
		}
		runs, err := p.RunsForSHA(ctx, o.SHA)
		if err != nil {
			return model.Verdict{}, err
		}
		now := clk.Now()
		if len(runs) == 0 {
			// Only conclude no_runs before we have ever seen a run for this sha; a
			// transient empty list after runs appeared must not read as green.
			if !seenRuns && now.Sub(callStart) >= o.StartupGrace {
				return model.Verdict{SHA: o.SHA, Status: "completed", Conclusion: "no_runs",
					Runs: []model.RunOutcome{},
					Note: "no workflow runs found for this sha (nothing triggered on this event, or the push has not registered yet)"}, nil
			}
		} else {
			seenRuns = true
			if allCompleted(runs) {
				return terminalVerdict(ctx, p, o.SHA, runs, elapsedS(clk, earliestStart(runs)))
			}
			// The deadline measures how long the still-unfinished runs have been
			// going — not legs that already completed (e.g. an earlier stage of a
			// workflow chain), whose old start would time us out prematurely. A
			// not-yet-started (queued, zero StartedAt) run leaves it to MaxBlock.
			if ws := earliestIncompleteStart(runs); !ws.IsZero() && now.Sub(ws) >= o.Timeout {
				return pendingVerdict(o.SHA, "timed_out", runs, elapsedS(clk, earliestStart(runs))), nil
			}
		}
		if now.Sub(callStart) >= o.MaxBlock {
			return pendingVerdict(o.SHA, "pending", runs, elapsedS(clk, earliestStart(runs))), nil
		}
		// Never sleep past the per-call ceiling, or a large --interval would let
		// the shell's 600s kill land before we return pending.
		d := o.Interval
		if rem := o.MaxBlock - now.Sub(callStart); rem < d {
			d = rem
		}
		if err := clk.Sleep(ctx, d); err != nil {
			return model.Verdict{}, err
		}
	}
}

func elapsedS(clk Clock, start time.Time) int {
	if start.IsZero() {
		return 0
	}
	d := clk.Now().Sub(start)
	if d < 0 {
		d = 0
	}
	return int(d.Seconds())
}

func earliestStart(runs []RunState) time.Time {
	var min time.Time
	for _, r := range runs {
		if r.StartedAt.IsZero() {
			continue
		}
		if min.IsZero() || r.StartedAt.Before(min) {
			min = r.StartedAt
		}
	}
	return min
}

// earliestIncompleteStart is earliestStart restricted to runs that have not yet
// completed — the ones the deadline is actually waiting on.
func earliestIncompleteStart(runs []RunState) time.Time {
	var min time.Time
	for _, r := range runs {
		if r.Status == "completed" || r.StartedAt.IsZero() {
			continue
		}
		if min.IsZero() || r.StartedAt.Before(min) {
			min = r.StartedAt
		}
	}
	return min
}

func allCompleted(runs []RunState) bool {
	for _, r := range runs {
		if r.Status != "completed" {
			return false
		}
	}
	return true
}

func summaries(runs []RunState) []model.RunOutcome {
	outs := make([]model.RunOutcome, len(runs))
	for i, r := range runs {
		outs[i] = model.RunOutcome{ID: r.ID, Name: r.Name, Status: r.Status,
			Conclusion: r.Conclusion, Event: r.Event, HTMLURL: r.HTMLURL}
	}
	return outs
}

func pendingVerdict(sha, conclusion string, runs []RunState, elapsed int) model.Verdict {
	return model.Verdict{SHA: sha, Status: "in_progress", Conclusion: conclusion,
		ElapsedS: elapsed, Runs: summaries(runs)}
}

// runFailed reports conclusions that make the overall verdict red.
func runFailed(conclusion string) bool {
	switch conclusion {
	case "failure", "startup_failure", "timed_out", "action_required", "stale":
		return true
	}
	return false
}

func overallConclusion(runs []RunState) string {
	red, cancelled := false, false
	for _, r := range runs {
		if runFailed(r.Conclusion) {
			red = true
		}
		if r.Conclusion == "cancelled" {
			cancelled = true
		}
	}
	switch {
	case red:
		return "failure"
	case cancelled:
		return "cancelled"
	default:
		return "success"
	}
}

func terminalVerdict(ctx context.Context, p Poller, sha string, runs []RunState, elapsed int) (model.Verdict, error) {
	outs := summaries(runs)
	var budget model.Budget
	var notes []string
	for i, r := range runs {
		if r.Conclusion != "failure" {
			if runFailed(r.Conclusion) { // red but no downloadable step logs (e.g. startup_failure)
				notes = append(notes, r.Name+": "+r.Conclusion+" (see "+r.HTMLURL+")")
			}
			continue
		}
		res, err := p.Excerpts(ctx, r.ID)
		if err != nil {
			return model.Verdict{}, err
		}
		if res == nil {
			continue
		}
		outs[i].Jobs = res.Jobs
		budget.LimitBytes = res.Budget.LimitBytes
		budget.UsedBytes += res.Budget.UsedBytes
		budget.OmittedLines += res.Budget.OmittedLines
		if res.Note != "" {
			notes = append(notes, r.Name+": "+res.Note)
		}
	}
	v := model.Verdict{SHA: sha, Status: "completed", Conclusion: overallConclusion(runs),
		ElapsedS: elapsed, Runs: outs, Budget: budget}
	if len(notes) > 0 {
		v.Note = strings.Join(notes, "; ")
	}
	return v, nil
}

// ExitCode maps a verdict's conclusion to cifail's process exit code. wait extends
// the extract contract with 124 (not concluded); the JSON conclusion disambiguates
// pending (re-run to resume) from timed_out (give up).
func ExitCode(v model.Verdict) core.Code {
	switch v.Conclusion {
	case "success", "no_runs":
		return core.CodeOK // 0
	case "failure", "cancelled":
		return core.Code(1) // red — CI concluded failure/cancelled; shell && stops here
	case "pending", "timed_out":
		return core.CodeNotConcluded // 124
	default:
		return core.CodeAPI // 3 — unexpected conclusion
	}
}
