package gh

import (
	"context"
	"fmt"
	"net/url"
)

// LastGreenRun returns the newest successful run of the given workflow on the
// branch — the baseline `delta` diffs a failing run against. The bool reports
// whether one exists: a branch with no green history is a legitimate degrade
// (delta still produces a report), not an error. The workflow-scoped endpoint
// filters server-side, so one call suffices and no client window can miss an
// older green.
func (c *Client) LastGreenRun(ctx context.Context, workflowID int64, branch string) (RunSummary, bool, error) {
	v := url.Values{"branch": {branch}, "status": {"success"}, "per_page": {"1"}}
	var list apiRunList
	path := c.repoPath(fmt.Sprintf("/actions/workflows/%d/runs?%s", workflowID, v.Encode()))
	if err := c.getJSON(ctx, path, &list); err != nil {
		return RunSummary{}, false, err
	}
	if len(list.Runs) == 0 {
		return RunSummary{}, false, nil
	}
	r := list.Runs[0]
	return RunSummary{
		ID: r.ID, Name: r.Name, Status: r.Status, Conclusion: r.Conclusion,
		Event: r.Event, HTMLURL: r.HTMLURL, StartedAt: r.RunStartedAt, HeadSHA: r.HeadSHA,
	}, true, nil
}
