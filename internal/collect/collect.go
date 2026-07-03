// Package collect orchestrates a cifail run: it drives the gh client to resolve
// the failing run, its failed jobs/steps, logs, and annotations, then runs the
// extract budget algorithm and assembles the model.Result an agent consumes.
package collect

import (
	"context"

	"github.com/akira-toriyama/cifail/internal/extract"
	"github.com/akira-toriyama/cifail/internal/gh"
	"github.com/akira-toriyama/cifail/internal/model"
)

// Collect resolves the target's failing run and returns the assembled result.
// A soft miss (no failing run) surfaces as a core.CodeNoFailure error from the
// gh client; a failed run with no jobs (a workflow-file syntax error) returns a
// result carrying only run metadata and a Note pointing at html_url. Each gh
// call is bound to ctx, so an interrupt aborts an in-flight extraction.
func Collect(ctx context.Context, c *gh.Client, t gh.Target, cfg extract.Config) (*model.Result, error) {
	run, err := c.ResolveRun(ctx, t)
	if err != nil {
		return nil, err
	}

	failedJobs, err := c.FailedJobs(ctx, run.ID)
	if err != nil {
		return nil, err
	}

	result := &model.Result{Run: run, Budget: model.Budget{LimitBytes: cfg.BudgetBytes}}

	// Edge case confirmed to exist in the wild: a failed run with zero failing
	// jobs (the workflow file itself didn't parse). There are no step logs to
	// extract, so hand the caller the run URL.
	if len(failedJobs) == 0 {
		result.Note = "this run failed with no failing job — usually a workflow-file syntax error; open html_url for details"
		result.Jobs = []model.Job{}
		return result, nil
	}

	archive, err := c.FetchLogs(ctx, run.ID)
	if err != nil {
		return nil, err
	}

	// Flatten every failed step across jobs into one ordered list so the extract
	// budget is global with tail-bias (the last failing step gets first claim).
	type ref struct{ job int }
	var inputs []extract.StepInput
	var refs []ref

	jobs := make([]model.Job, len(failedJobs))
	for ji, fj := range failedJobs {
		jobs[ji] = model.Job{ID: fj.ID, Name: fj.Name, Conclusion: fj.Conclusion, HTMLURL: fj.HTMLURL}

		if len(fj.Steps) == 0 {
			// Job failed outside a step (cancelled, infra). Use the whole job log.
			log, _ := archive.JobLog(fj.Name)
			inputs = append(inputs, extract.StepInput{Number: 0, Name: "(job log)", Log: log})
			refs = append(refs, ref{ji})
			continue
		}

		for si, s := range fj.Steps {
			log, ok := archive.StepLog(fj.Name, s.Number)
			if !ok {
				// No per-step file in the archive: fall back to the whole job
				// log, but attach it only to the LAST failed step so a large log
				// isn't duplicated across a job's failed steps.
				if si == len(fj.Steps)-1 {
					log, _ = archive.JobLog(fj.Name)
				}
			}
			inputs = append(inputs, extract.StepInput{Number: s.Number, Name: s.Name, Log: log})
			refs = append(refs, ref{ji})
		}
	}

	outs, budget := extract.Extract(inputs, cfg)
	result.Budget = budget

	for k, o := range outs {
		jobs[refs[k].job].FailedSteps = append(jobs[refs[k].job].FailedSteps, model.FailedStep{
			Number:       o.Number,
			Name:         o.Name,
			Excerpts:     o.Excerpts,
			OmittedLines: o.OmittedLines,
		})
	}

	// Merge annotations (cheap, sparse). Attach each job's annotations to its
	// first failed step so the caller sees the matcher's finding alongside the
	// excerpts.
	for ji, fj := range failedJobs {
		anns := c.Annotations(ctx, fj.ID)
		if len(anns) > 0 && len(jobs[ji].FailedSteps) > 0 {
			jobs[ji].FailedSteps[0].Annotations = anns
		}
	}

	// Annotations is best-effort and swallows its errors, so a Ctrl-C during that
	// loop would otherwise slip through as a "successful" result and exit 0. Surface
	// the cancellation here so an interrupt still maps to the silent 130 exit.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result.Jobs = jobs
	return result, nil
}
