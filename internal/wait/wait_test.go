package wait

import (
	"errors"
	"testing"
	"time"

	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/model"
)

var epoch = time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)

// fakeClock advances Now by each Sleep; Now starts at epoch (no wall clock).
type fakeClock struct{ t time.Time }

func newClock() *fakeClock                 { return &fakeClock{t: epoch} }
func (c *fakeClock) Now() time.Time        { return c.t }
func (c *fakeClock) Sleep(d time.Duration) { c.t = c.t.Add(d) }

// fakePoller returns scripted run states per poll (last entry repeats) and canned
// excerpts per run id.
type fakePoller struct {
	polls    [][]RunState
	i        int
	excerpts map[int64]*model.Result
	pollErr  error
}

func (p *fakePoller) RunsForSHA(string) ([]RunState, error) {
	if p.pollErr != nil {
		return nil, p.pollErr
	}
	r := p.polls[p.i]
	if p.i < len(p.polls)-1 {
		p.i++
	}
	return r, nil
}
func (p *fakePoller) Excerpts(id int64) (*model.Result, error) { return p.excerpts[id], nil }

func opts() Options {
	return Options{SHA: "sha", Timeout: 300 * time.Second, Interval: 10 * time.Second,
		MaxBlock: 540 * time.Second, StartupGrace: 60 * time.Second}
}
func completed(id int64, name, concl string) RunState {
	return RunState{ID: id, Name: name, Status: "completed", Conclusion: concl, StartedAt: epoch}
}
func running(id int64, name string) RunState {
	return RunState{ID: id, Name: name, Status: "in_progress", StartedAt: epoch}
}

func TestGreen(t *testing.T) {
	p := &fakePoller{polls: [][]RunState{{completed(1, "ci", "success"), completed(2, "lint", "success")}}}
	v, err := Run(p, newClock(), opts())
	if err != nil {
		t.Fatal(err)
	}
	if v.Conclusion != "success" || v.Status != "completed" {
		t.Fatalf("got %+v", v)
	}
	if ExitCode(v) != core.CodeOK {
		t.Errorf("exit = %d, want 0", ExitCode(v))
	}
	if v.Runs[0].Jobs != nil {
		t.Errorf("green run must carry no jobs")
	}
}

func TestRedEmbedsExcerpts(t *testing.T) {
	p := &fakePoller{
		polls: [][]RunState{{completed(1, "ci", "failure"), completed(2, "lint", "success")}},
		excerpts: map[int64]*model.Result{1: {
			Jobs: []model.Job{{ID: 9, Name: "build", Conclusion: "failure",
				FailedSteps: []model.FailedStep{{Number: 6, Name: "go test",
					Excerpts: []model.Excerpt{{StartLine: 40, Reason: "match", Lines: []string{"--- FAIL"}}}}}}},
			Budget: model.Budget{LimitBytes: 8192, UsedBytes: 500, OmittedLines: 3},
		}},
	}
	v, err := Run(p, newClock(), opts())
	if err != nil {
		t.Fatal(err)
	}
	if v.Conclusion != "failure" {
		t.Fatalf("conclusion = %q", v.Conclusion)
	}
	if ExitCode(v) != core.Code(1) {
		t.Errorf("exit = %d, want 1", ExitCode(v))
	}
	if len(v.Runs[0].Jobs) != 1 || v.Runs[0].Jobs[0].FailedSteps[0].Excerpts[0].Lines[0] != "--- FAIL" {
		t.Errorf("excerpts not embedded: %+v", v.Runs[0])
	}
	if v.Runs[1].Jobs != nil {
		t.Errorf("success run must carry no jobs")
	}
	if v.Budget.UsedBytes != 500 {
		t.Errorf("budget not aggregated: %+v", v.Budget)
	}
}

func TestWorstOf(t *testing.T) {
	cases := []struct {
		name string
		runs []RunState
		want string
	}{
		{"cancelled beats success", []RunState{completed(1, "a", "success"), completed(2, "b", "cancelled")}, "cancelled"},
		{"failure beats cancelled", []RunState{completed(1, "a", "cancelled"), completed(2, "b", "failure")}, "failure"},
		{"neutral+skipped is success", []RunState{completed(1, "a", "neutral"), completed(2, "b", "skipped")}, "success"},
		{"startup_failure is red", []RunState{completed(1, "a", "startup_failure")}, "failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakePoller{polls: [][]RunState{tc.runs}, excerpts: map[int64]*model.Result{}}
			for _, r := range tc.runs {
				if r.Conclusion == "failure" {
					p.excerpts[r.ID] = &model.Result{}
				}
			}
			v, err := Run(p, newClock(), opts())
			if err != nil {
				t.Fatal(err)
			}
			if v.Conclusion != tc.want {
				t.Errorf("got %q, want %q", v.Conclusion, tc.want)
			}
		})
	}
}

func TestPending(t *testing.T) {
	o := opts()
	o.MaxBlock = 50 * time.Second
	p := &fakePoller{polls: [][]RunState{{running(1, "ci")}}}
	v, err := Run(p, newClock(), o)
	if err != nil {
		t.Fatal(err)
	}
	if v.Conclusion != "pending" || v.Status != "in_progress" {
		t.Fatalf("got %+v", v)
	}
	if ExitCode(v) != core.CodeNotConcluded {
		t.Errorf("exit = %d, want 124", ExitCode(v))
	}
}

func TestTimedOut(t *testing.T) {
	o := opts()
	o.Timeout = 30 * time.Second
	p := &fakePoller{polls: [][]RunState{{running(1, "ci")}}}
	v, err := Run(p, newClock(), o)
	if err != nil {
		t.Fatal(err)
	}
	if v.Conclusion != "timed_out" {
		t.Fatalf("got %+v", v)
	}
	if ExitCode(v) != core.CodeNotConcluded {
		t.Errorf("exit = %d, want 124", ExitCode(v))
	}
}

func TestNoRuns(t *testing.T) {
	o := opts()
	o.StartupGrace = 30 * time.Second
	p := &fakePoller{polls: [][]RunState{{}}}
	v, err := Run(p, newClock(), o)
	if err != nil {
		t.Fatal(err)
	}
	if v.Conclusion != "no_runs" || ExitCode(v) != core.CodeOK {
		t.Fatalf("got %+v (exit %d)", v, ExitCode(v))
	}
}

func TestRunsAppearAfterGrace(t *testing.T) {
	p := &fakePoller{polls: [][]RunState{{}, {completed(1, "ci", "success")}}}
	v, err := Run(p, newClock(), opts())
	if err != nil {
		t.Fatal(err)
	}
	if v.Conclusion != "success" {
		t.Fatalf("got %+v", v)
	}
}

func TestPollErrorPropagates(t *testing.T) {
	p := &fakePoller{pollErr: core.APIf("boom")}
	_, err := Run(p, newClock(), opts())
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != core.CodeAPI {
		t.Fatalf("got %v, want CodeAPI", err)
	}
}

func TestTransientEmptyAfterRunsDoesNotFalseNoRuns(t *testing.T) {
	// Runs appear (in_progress), then a poll returns empty (GitHub list
	// eventual-consistency). Must NOT read as no_runs/green; keep waiting and hit
	// the per-call ceiling.
	o := opts()
	o.StartupGrace = 30 * time.Second
	o.MaxBlock = 100 * time.Second
	p := &fakePoller{polls: [][]RunState{{running(1, "ci")}, {}}} // then empty forever
	v, err := Run(p, newClock(), o)
	if err != nil {
		t.Fatal(err)
	}
	if v.Conclusion == "no_runs" {
		t.Fatalf("transient empty after seeing runs must not be no_runs: %+v", v)
	}
	if v.Conclusion != "pending" {
		t.Fatalf("want pending, got %+v", v)
	}
}

func TestTimeoutMeasuredFromIncompleteRun(t *testing.T) {
	// An earlier chain leg completed long ago; a later leg is still running from
	// ~now. The deadline must track the running leg, not fire off the old leg's
	// start.
	oldDone := RunState{ID: 1, Name: "build", Status: "completed", Conclusion: "success",
		StartedAt: epoch.Add(-40 * time.Minute)}
	later := RunState{ID: 2, Name: "release", Status: "in_progress", StartedAt: epoch}
	laterDone := RunState{ID: 2, Name: "release", Status: "completed", Conclusion: "success", StartedAt: epoch}
	o := opts() // Timeout 300s — old leg is 40m old, running leg is 0s old
	p := &fakePoller{polls: [][]RunState{{oldDone, later}, {oldDone, laterDone}}}
	v, err := Run(p, newClock(), o)
	if err != nil {
		t.Fatal(err)
	}
	if v.Conclusion != "success" {
		t.Fatalf("must not time out on the old completed leg; got %+v", v)
	}
}

func TestQueuedRunDoesNotFalseTimeout(t *testing.T) {
	// A queued run has a zero StartedAt; the overall deadline must not fire off a
	// bogus huge elapsed. It should keep polling and hit the per-call ceiling.
	queued := RunState{ID: 1, Name: "ci", Status: "queued"} // StartedAt zero
	o := opts()
	o.MaxBlock = 50 * time.Second
	p := &fakePoller{polls: [][]RunState{{queued}}}
	v, err := Run(p, newClock(), o)
	if err != nil {
		t.Fatal(err)
	}
	if v.Conclusion != "pending" {
		t.Fatalf("queued run must yield pending, got %+v", v)
	}
}
