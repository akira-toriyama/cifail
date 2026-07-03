package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVerdictSuccessOmitsJobsAndNote(t *testing.T) {
	v := Verdict{
		SHA: "abc123", Status: "completed", Conclusion: "success", ElapsedS: 210,
		Runs:   []RunOutcome{{ID: 1, Name: "ci", Status: "completed", Conclusion: "success"}},
		Budget: Budget{LimitBytes: 8192},
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if strings.Contains(s, `"jobs"`) {
		t.Errorf("success run must omit jobs: %s", s)
	}
	if strings.Contains(s, `"note"`) {
		t.Errorf("empty note must be omitted: %s", s)
	}
	if !strings.Contains(s, `"conclusion":"success"`) {
		t.Errorf("conclusion missing: %s", s)
	}
}

func TestVerdictFailureCarriesExcerpts(t *testing.T) {
	v := Verdict{
		SHA: "abc123", Status: "completed", Conclusion: "failure", ElapsedS: 312,
		Runs: []RunOutcome{{
			ID: 2, Name: "ci", Status: "completed", Conclusion: "failure",
			Jobs: []Job{{ID: 9, Name: "build", Conclusion: "failure",
				FailedSteps: []FailedStep{{Number: 6, Name: "go test",
					Excerpts: []Excerpt{{StartLine: 40, Reason: "match", Lines: []string{"--- FAIL"}}}}}}},
		}},
	}
	b, _ := json.Marshal(v)
	if !strings.Contains(string(b), "--- FAIL") {
		t.Errorf("excerpt lines must survive marshal: %s", b)
	}
}
