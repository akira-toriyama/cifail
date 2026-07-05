package gh

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// RunSummary is a workflow run's status snapshot for `wait`: enough to poll for
// completion, compute elapsed from StartedAt, and (for failing runs) fetch
// excerpts by ID.
type RunSummary struct {
	ID         int64
	Name       string
	Status     string // queued | in_progress | completed
	Conclusion string
	Event      string
	HTMLURL    string
	StartedAt  time.Time
}

// BranchRuns lists up to perPage of the most recent workflow runs on a branch
// (newest first, single page) for the base-rate tier. The bool reports whether
// the branch has MORE runs than were fetched, so a truncated base-rate survey
// isn't mistaken for a full one.
func (c *Client) BranchRuns(ctx context.Context, branch string, perPage int) ([]RunSummary, bool, error) {
	v := url.Values{"branch": {branch}, "per_page": {fmt.Sprint(perPage)}}
	var list apiRunList
	if err := c.getJSON(ctx, c.repoPath("/actions/runs?"+v.Encode()), &list); err != nil {
		return nil, false, err
	}
	out := make([]RunSummary, 0, len(list.Runs))
	for _, r := range list.Runs {
		out = append(out, RunSummary{
			ID: r.ID, Name: r.Name, Status: r.Status, Conclusion: r.Conclusion,
			Event: r.Event, HTMLURL: r.HTMLURL, StartedAt: r.RunStartedAt,
		})
	}
	return out, list.TotalCount > len(out), nil
}

// RunsForSHA lists every workflow run for the head sha (any status), paginating,
// and returns their status snapshots.
func (c *Client) RunsForSHA(ctx context.Context, sha string) ([]RunSummary, error) {
	var out []RunSummary
	page := 1
	for {
		v := url.Values{"head_sha": {sha}, "per_page": {"100"}, "page": {fmt.Sprint(page)}}
		var list apiRunList
		if err := c.getJSON(ctx, c.repoPath("/actions/runs?"+v.Encode()), &list); err != nil {
			return nil, err
		}
		for _, r := range list.Runs {
			out = append(out, RunSummary{
				ID: r.ID, Name: r.Name, Status: r.Status, Conclusion: r.Conclusion,
				Event: r.Event, HTMLURL: r.HTMLURL, StartedAt: r.RunStartedAt,
			})
		}
		if len(out) >= list.TotalCount || len(list.Runs) == 0 {
			break
		}
		page++
	}
	return out, nil
}
