package gh

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/model"
)

// Target selects which run to inspect. Exactly one of RunID / PR / Branch is
// used, in that precedence; a zero Target is invalid (the CLI fills Branch with
// the current branch as the default).
type Target struct {
	RunID  int64
	PR     int
	Branch string
}

// apiRun is the subset of a workflow-run object cifail needs.
type apiRun struct {
	ID           int64     `json:"id"`
	WorkflowID   int64     `json:"workflow_id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	Event        string    `json:"event"`
	HeadBranch   string    `json:"head_branch"`
	HeadSHA      string    `json:"head_sha"`
	RunAttempt   int       `json:"run_attempt"`
	HTMLURL      string    `json:"html_url"`
	RunStartedAt time.Time `json:"run_started_at"`
}

type apiRunList struct {
	TotalCount int      `json:"total_count"`
	Runs       []apiRun `json:"workflow_runs"`
}

type apiPull struct {
	Head struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
}

// ResolveRun finds the run to extract and returns its metadata. For an explicit
// RunID it fetches that run directly; for a PR or branch it queries the run list
// for the target's head and returns the newest FAILING run. A soft miss (no
// failing run) is a core.CodeNoFailure error.
func (c *Client) ResolveRun(ctx context.Context, t Target) (model.Run, error) {
	switch {
	case t.RunID != 0:
		var r apiRun
		if err := c.getJSON(ctx, c.repoPath(fmt.Sprintf("/actions/runs/%d", t.RunID)), &r); err != nil {
			return model.Run{}, err
		}
		if r.Conclusion != "failure" {
			return toRun(r), core.NoFailuref("run %d did not fail (conclusion=%q)", t.RunID, r.Conclusion)
		}
		return toRun(r), nil

	case t.PR != 0:
		var pr apiPull
		if err := c.getJSON(ctx, c.repoPath(fmt.Sprintf("/pulls/%d", t.PR)), &pr); err != nil {
			return model.Run{}, err
		}
		if pr.Head.SHA == "" {
			return model.Run{}, core.APIf("PR #%d has no head SHA", t.PR)
		}
		return c.latestFailure(ctx, url.Values{"head_sha": {pr.Head.SHA}}, fmt.Sprintf("PR #%d", t.PR))

	case t.Branch != "":
		return c.latestFailure(ctx, url.Values{"branch": {t.Branch}}, fmt.Sprintf("branch %q", t.Branch))

	default:
		return model.Run{}, core.Usagef("no target: pass --run, --pr, or --branch (or run inside a repo checkout)")
	}
}

// latestFailure queries the run list with the given filter and returns the
// newest run whose conclusion is failure.
func (c *Client) latestFailure(ctx context.Context, filter url.Values, desc string) (model.Run, error) {
	filter.Set("per_page", "30")
	var list apiRunList
	if err := c.getJSON(ctx, c.repoPath("/actions/runs?"+filter.Encode()), &list); err != nil {
		return model.Run{}, err
	}
	// The API returns runs newest-first; take the first failure.
	for _, r := range list.Runs {
		if r.Status == "completed" && r.Conclusion == "failure" {
			return toRun(r), nil
		}
	}
	return model.Run{}, core.NoFailuref("no failing run found for %s", desc)
}

func toRun(r apiRun) model.Run {
	return model.Run{
		ID:         r.ID,
		WorkflowID: r.WorkflowID,
		Name:       r.Name,
		Status:     r.Status,
		Conclusion: r.Conclusion,
		Event:      r.Event,
		HeadBranch: r.HeadBranch,
		HeadSHA:    r.HeadSHA,
		Attempt:    r.RunAttempt,
		HTMLURL:    r.HTMLURL,
	}
}

// toRunSummary projects an apiRun onto the RunSummary the wait/flake/delta
// features consume. Centralizing the mapping keeps the three call sites
// (BranchRuns, RunsForSHA, LastGreenRun) from drifting field-by-field.
func toRunSummary(r apiRun) RunSummary {
	return RunSummary{
		ID:         r.ID,
		WorkflowID: r.WorkflowID,
		Name:       r.Name,
		Status:     r.Status,
		Conclusion: r.Conclusion,
		Event:      r.Event,
		HTMLURL:    r.HTMLURL,
		StartedAt:  r.RunStartedAt,
		HeadSHA:    r.HeadSHA,
	}
}
