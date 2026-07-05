package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// last_green is the load-bearing field an agent branches on, so its absence
// must render as an explicit null — while optional context blocks
// (commit_range, environment) are omitted entirely, per the house omitempty
// discipline.
func TestDeltaReportNullsAndOmissions(t *testing.T) {
	r := DeltaReport{
		Failing: DeltaRun{Run: 1, SHA: "abc"},
		Jobs:    DeltaJobs{NewlyFailing: []string{}},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"last_green":null`, `"newly_failing":[]`, `"same_commit":false`, `"budget"`} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %s: %s", want, s)
		}
	}
	for _, absent := range []string{`"commit_range"`, `"environment"`, `"note"`} {
		if strings.Contains(s, absent) {
			t.Errorf("output should omit %s: %s", absent, s)
		}
	}
}

// The commit-range arrays are always semantically present (an empty survey is
// [] — a finding, not missing data), so they must never render as null.
func TestDeltaCommitRangeArraysRenderEmpty(t *testing.T) {
	cr := DeltaCommitRange{
		TopDirs:         []string{},
		Lockfiles:       []DeltaLockfile{},
		WorkflowChanges: []DeltaWorkflowChange{},
	}
	b, err := json.Marshal(cr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"top_dirs":[]`, `"lockfiles":[]`, `"workflow_changes":[]`} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %s: %s", want, s)
		}
	}
}
