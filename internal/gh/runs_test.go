package gh

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
)

func TestRunsForSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("head_sha"); got != "deadbeef" {
			t.Errorf("head_sha = %q, want deadbeef", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":2,"workflow_runs":[
			{"id":1,"name":"ci","status":"completed","conclusion":"failure","event":"push","html_url":"h1","run_started_at":"2026-07-03T00:00:00Z"},
			{"id":2,"name":"lint","status":"in_progress","conclusion":null,"run_started_at":"2026-07-03T00:00:05Z"}]}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	runs, err := c.RunsForSHA("deadbeef")
	if err != nil {
		t.Fatalf("RunsForSHA: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].Conclusion != "failure" || runs[1].Status != "in_progress" {
		t.Errorf("unexpected runs: %+v", runs)
	}
	if runs[0].StartedAt.IsZero() {
		t.Errorf("run_started_at not parsed for run 0")
	}
}

func TestCurrentSHA(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	git("commit", "--allow-empty", "-m", "x")

	sha, err := CurrentSHA(dir)
	if err != nil {
		t.Fatalf("CurrentSHA: %v", err)
	}
	if len(sha) < 7 {
		t.Errorf("sha = %q, want a full hash", sha)
	}
}
