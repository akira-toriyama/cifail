package gh

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// AllJobs must keep PASSING jobs — that a same-named matrix job passed elsewhere
// on the same sha is the flake signal FailedJobs throws away.
func TestAllJobsKeepsPassingJobs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/actions/runs/42/jobs") {
			t.Errorf("path = %q, want .../actions/runs/42/jobs", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":2,"jobs":[
			{"id":1,"name":"test (ubuntu-latest, 20)","conclusion":"failure","html_url":"h1"},
			{"id":2,"name":"test (ubuntu-latest, 22)","conclusion":"success","html_url":"h2"}]}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	jobs, err := c.AllJobs(context.Background(), 42)
	if err != nil {
		t.Fatalf("AllJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2 (FailedJobs would drop the passing one)", len(jobs))
	}
	var pass *JobResult
	for i := range jobs {
		if jobs[i].Conclusion == "success" {
			pass = &jobs[i]
		}
	}
	if pass == nil {
		t.Fatalf("passing job dropped; jobs=%+v", jobs)
	}
	if pass.Name != "test (ubuntu-latest, 22)" || pass.HTMLURL != "h2" {
		t.Errorf("passing job = %+v, want the matrix name and url preserved", *pass)
	}
}

func TestAttemptJobs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/attempts/2/jobs") {
			t.Errorf("path = %q, want .../attempts/2/jobs", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":1,"jobs":[
			{"id":9,"name":"test (ubuntu-latest, 20)","conclusion":"success","html_url":"h9"}]}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	jobs := c.AttemptJobs(context.Background(), 42, 2)
	if len(jobs) != 1 || jobs[0].Conclusion != "success" || jobs[0].Name != "test (ubuntu-latest, 20)" {
		t.Fatalf("AttemptJobs = %+v, want one success job with the matrix name", jobs)
	}
}

// A never-rerun run or an out-of-range attempt legitimately 404s; AttemptJobs is
// best-effort (Annotations-style) so a 404 must yield nil, not fail the command.
func TestAttemptJobsBestEffortOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	if jobs := c.AttemptJobs(context.Background(), 42, 5); jobs != nil {
		t.Errorf("AttemptJobs on 404 = %+v, want nil", jobs)
	}
}

// BranchRuns reports capped=true when the branch has more runs than were fetched,
// so a truncated base-rate survey isn't mistaken for a full one.
func TestBranchRunsCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("branch"); got != "main" {
			t.Errorf("branch = %q, want main", got)
		}
		if got := r.URL.Query().Get("per_page"); got != "2" {
			t.Errorf("per_page = %q, want 2", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":5,"workflow_runs":[
			{"id":1,"status":"completed","conclusion":"failure"},
			{"id":2,"status":"completed","conclusion":"success"}]}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	runs, capped, err := c.BranchRuns(context.Background(), "main", 2)
	if err != nil {
		t.Fatalf("BranchRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if !capped {
		t.Errorf("capped = false, want true (total 5 > fetched 2)")
	}
}

// A branch whose whole history fits in the window is not capped.
func TestBranchRunsNotCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":2,"workflow_runs":[
			{"id":1,"status":"completed","conclusion":"success"},
			{"id":2,"status":"completed","conclusion":"failure"}]}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	_, capped, err := c.BranchRuns(context.Background(), "main", 20)
	if err != nil {
		t.Fatalf("BranchRuns: %v", err)
	}
	if capped {
		t.Errorf("capped = true, want false (total 2 <= window 20)")
	}
}
