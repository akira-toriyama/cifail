package gh

import (
	"context"
	"fmt"

	"github.com/akira-toriyama/cifail/internal/model"
)

// apiStep is one step within a job.
type apiStep struct {
	Number     int    `json:"number"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

// apiJob is the subset of a job object cifail needs.
type apiJob struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	Steps      []apiStep `json:"steps"`
}

type apiJobList struct {
	TotalCount int      `json:"total_count"`
	Jobs       []apiJob `json:"jobs"`
}

// FailedJob carries a failing job and its failing steps (raw metadata; logs and
// annotations are filled in later).
type FailedJob struct {
	ID          int64
	Name        string
	Conclusion  string
	HTMLURL     string
	Annotations []model.Annotation
	Steps       []FailedStep
}

// FailedStep identifies a failing step (its log text is attached later).
type FailedStep struct {
	Number int
	Name   string
}

// JobResult is a job's terminal state within a run or attempt: its display name
// (the matrix identity, e.g. "test (ubuntu-latest, 20)"), conclusion, and URL.
// Unlike FailedJob it keeps PASSING jobs — a same-named job passing on a sibling
// run or a rerun attempt is the same-sha-divergence signal `flake` verdicts on.
type JobResult struct {
	Name       string
	Conclusion string
	HTMLURL    string
}

// AllJobs lists every job of the run's LATEST attempt (paginating), keeping ALL
// conclusions — the passing jobs FailedJobs drops are the divergence signal.
func (c *Client) AllJobs(ctx context.Context, runID int64) ([]JobResult, error) {
	return c.listJobs(ctx, fmt.Sprintf("/actions/runs/%d/jobs", runID))
}

// AttemptJobs lists the jobs of a specific prior attempt. It is best-effort: a
// never-rerun run or an out-of-range attempt number legitimately 404s, so any
// error yields nil rather than failing the whole command (cf. Annotations).
func (c *Client) AttemptJobs(ctx context.Context, runID int64, attempt int) []JobResult {
	jobs, err := c.listJobs(ctx, fmt.Sprintf("/actions/runs/%d/attempts/%d/jobs", runID, attempt))
	if err != nil {
		return nil
	}
	return jobs
}

// listJobs paginates a jobs endpoint (an absolute path with no query) and
// returns every job's name/conclusion/url.
func (c *Client) listJobs(ctx context.Context, endpoint string) ([]JobResult, error) {
	var out []JobResult
	page := 1
	for {
		var list apiJobList
		path := c.repoPath(fmt.Sprintf("%s?per_page=100&page=%d", endpoint, page))
		if err := c.getJSON(ctx, path, &list); err != nil {
			return nil, err
		}
		for _, j := range list.Jobs {
			out = append(out, JobResult{Name: j.Name, Conclusion: j.Conclusion, HTMLURL: j.HTMLURL})
		}
		if len(out) >= list.TotalCount || len(list.Jobs) == 0 {
			break
		}
		page++
	}
	return out, nil
}

// FailedJobs lists the run's jobs (paginating) and keeps only the failing jobs
// and, within them, the failing steps. A job that failed with no failing step
// (e.g. it was cancelled or the failure was outside a step) is still returned
// with an empty Steps slice so the caller can fall back to the whole job log.
func (c *Client) FailedJobs(ctx context.Context, runID int64) ([]FailedJob, error) {
	var jobs []apiJob
	page := 1
	for {
		var list apiJobList
		path := c.repoPath(fmt.Sprintf("/actions/runs/%d/jobs?per_page=100&page=%d", runID, page))
		if err := c.getJSON(ctx, path, &list); err != nil {
			return nil, err
		}
		jobs = append(jobs, list.Jobs...)
		if len(jobs) >= list.TotalCount || len(list.Jobs) == 0 {
			break
		}
		page++
	}

	var failed []FailedJob
	for _, j := range jobs {
		if j.Conclusion != "failure" {
			continue
		}
		fj := FailedJob{ID: j.ID, Name: j.Name, Conclusion: j.Conclusion, HTMLURL: j.HTMLURL}
		for _, s := range j.Steps {
			if s.Conclusion == "failure" {
				fj.Steps = append(fj.Steps, FailedStep{Number: s.Number, Name: s.Name})
			}
		}
		failed = append(failed, fj)
	}
	return failed, nil
}
