// Package wait blocks until a commit's GitHub Actions workflow runs finish, then
// assembles a worst-of Verdict — embedding failed-step excerpts (via the existing
// extract pipeline) when red. It owns no IO: a Poller (gh-backed) and a Clock are
// injected, so the poll / timeout / resume / aggregation logic is unit-tested with
// fakes. Exit codes: 0 green|no_runs, 1 red, 124 pending|timed_out.
package wait

import (
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
	RunsForSHA(sha string) ([]RunState, error)
	Excerpts(runID int64) (*model.Result, error) // failing-run jobs + budget
}

// Clock is injected so tests neither sleep nor read the wall clock.
type Clock interface {
	Now() time.Time
	Sleep(d time.Duration)
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
func Run(p Poller, clk Clock, o Options) (model.Verdict, error) {
	callStart := clk.Now()
	for {
		runs, err := p.RunsForSHA(o.SHA)
		if err != nil {
			return model.Verdict{}, err
		}
		if len(runs) == 0 {
			if clk.Now().Sub(callStart) >= o.StartupGrace {
				return model.Verdict{SHA: o.SHA, Status: "completed", Conclusion: "no_runs",
					Note: "no workflow runs found for this sha (nothing triggered on this event, or the push has not registered yet)"}, nil
			}
			clk.Sleep(o.Interval)
			continue
		}
		start := earliestStart(runs)
		if allCompleted(runs) {
			return terminalVerdict(p, o.SHA, runs, elapsedS(clk, start))
		}
		// Honor the overall deadline only once a run has actually started — queued
		// runs have a zero StartedAt, and Now-zero would be a huge (false) elapsed.
		if !start.IsZero() && clk.Now().Sub(start) >= o.Timeout {
			return pendingVerdict(o.SHA, "timed_out", runs, elapsedS(clk, start)), nil
		}
		if clk.Now().Sub(callStart) >= o.MaxBlock {
			return pendingVerdict(o.SHA, "pending", runs, elapsedS(clk, start)), nil
		}
		clk.Sleep(o.Interval)
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

func terminalVerdict(p Poller, sha string, runs []RunState, elapsed int) (model.Verdict, error) {
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
		res, err := p.Excerpts(r.ID)
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
