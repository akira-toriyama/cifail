package collect

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/akira-toriyama/cifail/internal/extract"
	"github.com/akira-toriyama/cifail/internal/gh"
	"github.com/akira-toriyama/cifail/internal/model"
)

// fakeArchive keys logs by the exact job name Collect looks them up with (the
// real archive normalizes; here the caller controls both sides).
type fakeArchive struct {
	jobLogs  map[string]string
	stepLogs map[string]map[int]string
}

func (a fakeArchive) JobLog(name string) (string, bool) { s, ok := a.jobLogs[name]; return s, ok }

func (a fakeArchive) StepLog(name string, n int) (string, bool) {
	if m, ok := a.stepLogs[name]; ok {
		s, ok := m[n]
		return s, ok
	}
	return "", false
}

// fakeGH is a scripted ghAPI: no network, so Collect's orchestration branches are
// exercised directly.
type fakeGH struct {
	run        model.Run
	failedJobs []gh.FailedJob
	archive    logArchive
	anns       map[int64][]model.Annotation
}

func (f fakeGH) ResolveRun(context.Context, gh.Target) (model.Run, error) { return f.run, nil }
func (f fakeGH) FailedJobs(context.Context, int64) ([]gh.FailedJob, error) {
	return f.failedJobs, nil
}
func (f fakeGH) FetchLogs(context.Context, int64) (logArchive, error) { return f.archive, nil }
func (f fakeGH) Annotations(_ context.Context, jobID int64) []model.Annotation {
	return f.anns[jobID]
}

// A failing job with no failing step falls back to the whole-job log, surfaced as
// a single synthetic "(job log)" step.
func TestCollectJobWithNoStepsUsesJobLog(t *testing.T) {
	f := fakeGH{
		run:        model.Run{ID: 1, Conclusion: "failure"},
		failedJobs: []gh.FailedJob{{ID: 9, Name: "build", Conclusion: "failure"}},
		archive:    fakeArchive{jobLogs: map[string]string{"build": "##[error]boom"}},
	}
	res, err := collectFrom(context.Background(), f, gh.Target{RunID: 1}, extract.Default())
	if err != nil {
		t.Fatalf("collectFrom: %v", err)
	}
	if len(res.Jobs) != 1 || len(res.Jobs[0].FailedSteps) != 1 {
		t.Fatalf("jobs = %+v, want one job with one synthetic step", res.Jobs)
	}
	if got := res.Jobs[0].FailedSteps[0].Name; got != "(job log)" {
		t.Errorf("step name = %q, want %q", got, "(job log)")
	}
}

// When the archive has no per-step file, the whole-job log is attached to the
// LAST failed step only, so a big log isn't duplicated across a job's steps.
func TestCollectMissingStepFileAttachesJobLogToLastStep(t *testing.T) {
	f := fakeGH{
		run: model.Run{ID: 1, Conclusion: "failure"},
		failedJobs: []gh.FailedJob{{ID: 9, Name: "build", Conclusion: "failure", Steps: []gh.FailedStep{
			{Number: 3, Name: "compile"},
			{Number: 5, Name: "test"},
		}}},
		// Job log present, but NO per-step files.
		archive: fakeArchive{jobLogs: map[string]string{"build": "prep\n##[error]exit code 1\ndone"}},
	}
	res, err := collectFrom(context.Background(), f, gh.Target{RunID: 1}, extract.Default())
	if err != nil {
		t.Fatalf("collectFrom: %v", err)
	}
	steps := res.Jobs[0].FailedSteps
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	// Earlier step: no log, so no excerpts.
	if len(steps[0].Excerpts) != 0 {
		t.Errorf("step %d got excerpts %+v, want none (job log must attach to the last step only)", steps[0].Number, steps[0].Excerpts)
	}
	// Last step carries the fallback job log.
	if !excerptsContain(steps[1].Excerpts, "exit code 1") {
		t.Errorf("last step excerpts = %+v, want the fallback job log", steps[1].Excerpts)
	}
}

// A failed run with zero failing jobs (a workflow-file syntax error) yields run
// metadata plus a Note, and Jobs must serialize as [] not null.
func TestCollectZeroFailingJobsSetsNote(t *testing.T) {
	f := fakeGH{run: model.Run{ID: 1, Conclusion: "failure", HTMLURL: "u"}}
	res, err := collectFrom(context.Background(), f, gh.Target{RunID: 1}, extract.Default())
	if err != nil {
		t.Fatalf("collectFrom: %v", err)
	}
	if res.Note == "" {
		t.Error("Note should explain the zero-failing-job run")
	}
	if res.Jobs == nil {
		t.Error("Jobs must be non-nil so it renders as []")
	}
	if len(res.Jobs) != 0 {
		t.Errorf("Jobs = %+v, want empty", res.Jobs)
	}
}

// Annotations is best-effort and swallows its errors, so a Ctrl-C during that
// loop must still surface as a cancellation rather than a bogus success.
func TestCollectCancelledCtxSurfaces(t *testing.T) {
	f := fakeGH{
		run:        model.Run{ID: 1, Conclusion: "failure"},
		failedJobs: []gh.FailedJob{{ID: 9, Name: "build", Conclusion: "failure"}},
		archive:    fakeArchive{jobLogs: map[string]string{"build": "log"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := collectFrom(ctx, f, gh.Target{RunID: 1}, extract.Default())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func excerptsContain(exs []model.Excerpt, substr string) bool {
	for _, e := range exs {
		for _, l := range e.Lines {
			if strings.Contains(l, substr) {
				return true
			}
		}
	}
	return false
}
