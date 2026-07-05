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

// LastGreenRun asks the workflow-scoped runs endpoint so the server filters by
// workflow AND status — one call, no client-side window that could miss older
// greens.
func TestLastGreenRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/actions/workflows/7/runs") {
			t.Errorf("path = %q, want .../actions/workflows/7/runs", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("branch") != "main" || q.Get("status") != "success" || q.Get("per_page") != "1" {
			t.Errorf("query = %v, want branch=main status=success per_page=1", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":9,"workflow_runs":[
			{"id":120,"status":"completed","conclusion":"success","head_sha":"def","html_url":"g"}]}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	run, ok, err := c.LastGreenRun(context.Background(), 7, "main")
	if err != nil {
		t.Fatalf("LastGreenRun: %v", err)
	}
	if !ok || run.ID != 120 || run.HeadSHA != "def" {
		t.Fatalf("got ok=%v run=%+v, want ok with ID 120 / HeadSHA def", ok, run)
	}
}

// A branch with no green history is a legitimate degrade for delta (report
// with last_green: null, exit 0) — so "none found" must be ok=false, NOT an error.
func TestLastGreenRunNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":0,"workflow_runs":[]}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	_, ok, err := c.LastGreenRun(context.Background(), 7, "main")
	if err != nil {
		t.Fatalf("LastGreenRun: %v", err)
	}
	if ok {
		t.Error("ok = true, want false for an empty run list")
	}
}

// CompareCommits is delta's commit_range source: base = green sha, head =
// failing sha. behind_by > 0 is the rebase/force-push hidden-delta signal.
func TestCompareCommits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/compare/def...abc") {
			t.Errorf("path = %q, want .../compare/def...abc", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ahead_by":3,"behind_by":1,"total_commits":3,"files":[
			{"filename":"go.sum","additions":14,"deletions":9,"patch":"@@ -1 +1 @@"},
			{"filename":"src/api/x.go","additions":2,"deletions":0}]}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	cmp, err := c.CompareCommits(context.Background(), "def", "abc")
	if err != nil {
		t.Fatalf("CompareCommits: %v", err)
	}
	if cmp.AheadBy != 3 || cmp.BehindBy != 1 || cmp.TotalCommits != 3 {
		t.Errorf("counts = %+v, want ahead 3 / behind 1 / commits 3", cmp)
	}
	if len(cmp.Files) != 2 || cmp.Files[0].Path != "go.sum" || cmp.Files[0].Additions != 14 || cmp.Files[0].Patch == "" {
		t.Errorf("files = %+v, want go.sum first with stats and patch", cmp.Files)
	}
	if cmp.Capped {
		t.Error("Capped = true, want false for 2 files")
	}
}
