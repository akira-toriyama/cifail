package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func captureOut(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := out
	out = &buf
	defer func() { out = old }()
	fn()
	return buf.String()
}

// TestVersionNDJSON guards a regression: `--ndjson` used to be registered only
// on root's LOCAL flagset, so the `version` subcommand never inherited it and
// `cifail version --ndjson` errored at runtime with "unknown flag". The
// subcommand must own its own --ndjson and emit the commit/date-bearing JSON.
func TestVersionNDJSON(t *testing.T) {
	var got string
	err := func() error {
		var e error
		got = captureOut(t, func() {
			root := newRootCmd()
			root.SetArgs([]string{"version", "--ndjson"})
			e = root.Execute()
		})
		return e
	}()
	if err != nil {
		t.Fatalf("cifail version --ndjson: unexpected error: %v", err)
	}

	line := strings.TrimSpace(got)
	if strings.Contains(line, "\n") {
		t.Fatalf("--ndjson output must be a single line, got:\n%s", line)
	}
	var info map[string]any
	if uerr := json.Unmarshal([]byte(line), &info); uerr != nil {
		t.Fatalf("version --ndjson output is not valid JSON: %v\n%s", uerr, line)
	}
	if _, ok := info["version"]; !ok {
		t.Fatalf("compact version JSON is missing the \"version\" field: %s", line)
	}
}

// TestVersionHuman keeps the default (no --ndjson) as the human-readable line so
// the JSON branch stays opt-in.
func TestVersionHuman(t *testing.T) {
	got := captureOut(t, func() {
		root := newRootCmd()
		root.SetArgs([]string{"version"})
		if err := root.Execute(); err != nil {
			t.Fatalf("cifail version: unexpected error: %v", err)
		}
	})
	if !strings.HasPrefix(got, "cifail ") {
		t.Fatalf("plain `version` should print a `cifail <ver>` line, got: %q", got)
	}
	if strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Fatalf("plain `version` should not emit JSON, got: %q", got)
	}
}
