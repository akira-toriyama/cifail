package gh

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// delta needs the run's workflow identity to find "the last green run of the
// SAME workflow" — matching by display name is rename-fragile, so ResolveRun
// must carry workflow_id through.
func TestResolveRunCarriesWorkflowID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/actions/runs/42") {
			t.Errorf("path = %q, want .../actions/runs/42", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":42,"workflow_id":7,"status":"completed",
			"conclusion":"failure","head_branch":"main","head_sha":"abc","html_url":"h"}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	run, err := c.ResolveRun(context.Background(), Target{RunID: 42})
	if err != nil {
		t.Fatalf("ResolveRun: %v", err)
	}
	if run.WorkflowID != 7 {
		t.Errorf("WorkflowID = %d, want 7", run.WorkflowID)
	}
}

// delta diffs the green run's commit against the failing run's, so run
// summaries must keep the head sha the API already sends.
func TestBranchRunsCarryHeadSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":1,"workflow_runs":[
			{"id":1,"status":"completed","conclusion":"success","head_sha":"def456"}]}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	runs, _, err := c.BranchRuns(context.Background(), "main", 5)
	if err != nil {
		t.Fatalf("BranchRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].HeadSHA != "def456" {
		t.Fatalf("runs = %+v, want one run with HeadSHA def456", runs)
	}
}
