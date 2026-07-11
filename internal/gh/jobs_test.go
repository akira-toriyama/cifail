package gh

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FailedJobs keeps only failing jobs and, within them, only the failing steps —
// but a failing job with NO failing step is still returned with empty Steps so
// the caller can fall back to the whole job log.
func TestFailedJobsFiltersAndKeepsFailingSteps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/actions/runs/42/jobs") {
			t.Errorf("path = %q, want .../actions/runs/42/jobs", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":3,"jobs":[
			{"id":1,"name":"lint","conclusion":"success","steps":[
				{"number":1,"name":"vet","conclusion":"success"}]},
			{"id":2,"name":"test","conclusion":"failure","html_url":"h2","steps":[
				{"number":3,"name":"build","conclusion":"success"},
				{"number":5,"name":"go test","conclusion":"failure"}]},
			{"id":3,"name":"deploy","conclusion":"failure","html_url":"h3","steps":[]}]}`)
	}))
	defer srv.Close()

	failed, err := testClient(srv).FailedJobs(context.Background(), 42)
	if err != nil {
		t.Fatalf("FailedJobs: %v", err)
	}
	if len(failed) != 2 {
		t.Fatalf("got %d failed jobs, want 2 (the success job dropped)", len(failed))
	}
	if failed[0].Name != "test" || len(failed[0].Steps) != 1 {
		t.Fatalf("first failed job = %+v, want 'test' with one failing step", failed[0])
	}
	if s := failed[0].Steps[0]; s.Number != 5 || s.Name != "go test" {
		t.Errorf("kept step = %+v, want the failing 'go test' step (number 5)", s)
	}
	// A failing job with no failing step keeps an empty Steps slice (job-log fallback).
	if failed[1].Name != "deploy" || len(failed[1].Steps) != 0 {
		t.Errorf("second failed job = %+v, want 'deploy' with no steps", failed[1])
	}
}

// FailedJobs paginates: a run with more jobs than one page walks every page.
func TestFailedJobsPaginates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = io.WriteString(w, `{"total_count":2,"jobs":[
				{"id":1,"name":"a","conclusion":"failure"}]}`)
		case "2":
			_, _ = io.WriteString(w, `{"total_count":2,"jobs":[
				{"id":2,"name":"b","conclusion":"failure"}]}`)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
			_, _ = io.WriteString(w, `{"total_count":2,"jobs":[]}`)
		}
	}))
	defer srv.Close()

	failed, err := testClient(srv).FailedJobs(context.Background(), 42)
	if err != nil {
		t.Fatalf("FailedJobs: %v", err)
	}
	if len(failed) != 2 || failed[0].Name != "a" || failed[1].Name != "b" {
		t.Fatalf("failed = %+v, want both pages' jobs a and b", failed)
	}
}
