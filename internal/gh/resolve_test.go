package gh

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akira-toriyama/cifail/internal/core"
)

func testClient(srv *httptest.Server) *Client {
	return &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
}

// latestFailure walks the newest-first run list and returns the first COMPLETED
// failure, skipping in-progress and success runs — the branch/PR resolution path.
func TestResolveRunBranchPicksNewestCompletedFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/actions/runs") {
			t.Errorf("path = %q, want .../actions/runs", r.URL.Path)
		}
		if got := r.URL.Query().Get("branch"); got != "main" {
			t.Errorf("branch = %q, want main", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":3,"workflow_runs":[
			{"id":1,"status":"in_progress","conclusion":null},
			{"id":2,"status":"completed","conclusion":"failure","head_sha":"abc","workflow_id":5,"html_url":"h2"},
			{"id":3,"status":"completed","conclusion":"success"}]}`)
	}))
	defer srv.Close()

	run, err := testClient(srv).ResolveRun(context.Background(), Target{Branch: "main"})
	if err != nil {
		t.Fatalf("ResolveRun: %v", err)
	}
	if run.ID != 2 || run.WorkflowID != 5 {
		t.Errorf("run = %+v, want the completed failure (id 2, workflow 5)", run)
	}
}

// A branch/PR with no failing run is a soft miss (exit 1), not an error — a
// stable point of the exit-code contract.
func TestResolveRunNoFailingRunIsSoftMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":1,"workflow_runs":[
			{"id":3,"status":"completed","conclusion":"success"}]}`)
	}))
	defer srv.Close()

	_, err := testClient(srv).ResolveRun(context.Background(), Target{Branch: "main"})
	if ce := core.AsError(err); ce == nil || ce.Code != core.CodeNoFailure {
		t.Fatalf("err = %v, want a CodeNoFailure soft miss", err)
	}
}

// An explicit --run whose conclusion is not failure is also a soft miss (the run
// metadata is returned so a caller can see why), exit 1.
func TestResolveRunByIDNonFailureIsSoftMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/actions/runs/42") {
			t.Errorf("path = %q, want .../actions/runs/42", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":42,"status":"completed","conclusion":"success","html_url":"h"}`)
	}))
	defer srv.Close()

	run, err := testClient(srv).ResolveRun(context.Background(), Target{RunID: 42})
	if ce := core.AsError(err); ce == nil || ce.Code != core.CodeNoFailure {
		t.Fatalf("err = %v, want a CodeNoFailure soft miss", err)
	}
	if run.ID != 42 {
		t.Errorf("run.ID = %d, want 42 (metadata still returned on a soft miss)", run.ID)
	}
}

// A PR whose head has no sha can't be resolved to a run — an API error, exit 3.
func TestResolveRunPRWithoutHeadSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/pulls/9") {
			t.Errorf("path = %q, want .../pulls/9", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"head":{"sha":"","ref":"feature"}}`)
	}))
	defer srv.Close()

	_, err := testClient(srv).ResolveRun(context.Background(), Target{PR: 9})
	if ce := core.AsError(err); ce == nil || ce.Code != core.CodeAPI {
		t.Fatalf("err = %v, want a CodeAPI error", err)
	}
}

// A zero Target reaches no network — it is a usage error (exit 2).
func TestResolveRunZeroTargetIsUsage(t *testing.T) {
	c := &Client{Owner: "o", Repo: "r", token: "t", http: http.DefaultClient, base: "http://127.0.0.1:0"}
	_, err := c.ResolveRun(context.Background(), Target{})
	if ce := core.AsError(err); ce == nil || ce.Code != core.CodeUsage {
		t.Fatalf("err = %v, want a CodeUsage error", err)
	}
}
