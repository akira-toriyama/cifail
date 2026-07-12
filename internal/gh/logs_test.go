package gh

import (
	"archive/zip"
	"bytes"
	"testing"

	"github.com/akira-toriyama/cifail/internal/core"
)

// zipArchive builds an in-memory run log archive from name->content entries,
// mirroring the layout FetchLogs downloads.
func zipArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %q: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// The archive keys logs by the SANITIZED job name in the file path, while the
// caller looks them up by the API DISPLAY name. normalize reconciles the two, so
// a matrix job's whole-job and per-step logs must both resolve by display name.
func TestLogArchiveResolvesByDisplayName(t *testing.T) {
	const job = "test (ubuntu-latest, 20)"
	a, err := parseLogZip(zipArchive(t, map[string]string{
		"1_test (ubuntu-latest, 20).txt":            "whole job log",
		"test (ubuntu-latest, 20)/2_Set up job.txt": "set up job log",
		"test (ubuntu-latest, 20)/6_go test.txt":    "go test log",
		"test (ubuntu-latest, 20)/system.txt":       "runner diagnostics", // ignored: no step number
	}))
	if err != nil {
		t.Fatalf("parseLogZip: %v", err)
	}

	if got, ok := a.JobLog(job); !ok || got != "whole job log" {
		t.Errorf("JobLog(%q) = (%q, %v), want the whole job log", job, got, ok)
	}
	if got, ok := a.StepLog(job, 6); !ok || got != "go test log" {
		t.Errorf("StepLog(%q, 6) = (%q, %v), want the go test step log", job, got, ok)
	}
	if got, ok := a.StepLog(job, 2); !ok || got != "set up job log" {
		t.Errorf("StepLog(%q, 2) = (%q, %v), want the set-up-job step log", job, got, ok)
	}
	// system.txt (no numeric step prefix) is neither a job nor a step log: it must
	// not clobber the whole-job log above, and JobLog still returns the real one.
	// Unknown job / unknown step are clean misses, not panics.
	if _, ok := a.JobLog("nonexistent"); ok {
		t.Error("JobLog(nonexistent) = ok, want miss")
	}
	if _, ok := a.StepLog(job, 999); ok {
		t.Error("StepLog step 999 = ok, want miss")
	}
}

// normalize is the crux of the fuzzy job-name match. It is deliberately lossy
// (lowercase + strip every non-alphanumeric run), which reconciles path
// sanitization but can also collapse two distinct matrix names to one key — a
// known limitation this test documents so a future change is a conscious choice.
func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"build", "build"},
		{"go test", "gotest"},
		{"test (ubuntu-latest, 20)", "testubuntulatest20"},
		{"Build-X", "buildx"},
	}
	for _, c := range cases {
		if got := normalize(c.in); got != c.want {
			t.Errorf("normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Documented collision: punctuation-only differences collapse to one key, so
	// these distinct matrix legs share an archive slot.
	if normalize("test (ubuntu, 2.0)") != normalize("test (ubuntu, 20)") {
		t.Error("expected the known normalize collision between '2.0' and '20' matrix names")
	}
}

// A corrupt/non-zip archive body is an API/IO failure, not a panic.
func TestParseLogZipRejectsNonZip(t *testing.T) {
	_, err := parseLogZip([]byte("this is not a zip archive"))
	if err == nil {
		t.Fatal("parseLogZip(non-zip) = nil error, want an API error")
	}
	if code := core.ExitCode(err); code != int(core.CodeAPI) {
		t.Errorf("exit code = %d, want %d (CodeAPI)", code, core.CodeAPI)
	}
}
