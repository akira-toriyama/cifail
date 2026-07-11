package flake

import "testing"

// boundedInt maps a fuzzed int into [0, max], guarding math.MinInt (whose
// negation overflows back to a negative).
func boundedInt(n, max int) int {
	if n < 0 {
		n = -n
	}
	if n < 0 {
		n = 0
	}
	return n % (max + 1)
}

// fuzzEvidence builds a bounded, production-reachable Evidence from fuzzed
// primitives so Decide is exercised across the honesty-gate boundary both ways.
func fuzzEvidence(nJobs, nAttempts, nSiblings, branchRuns, branchFailures int, workflow, event string) Evidence {
	nJobs = boundedInt(nJobs, 8)
	nAttempts = boundedInt(nAttempts, 6)
	nSiblings = boundedInt(nSiblings, 6)
	runs := boundedInt(branchRuns, 100)
	failures := 0
	if runs > 0 {
		failures = boundedInt(branchFailures, runs) // <= runs, so rate stays in [0,1]
	}

	names := make([]string, nJobs)
	target := make([]JobConclusion, nJobs)
	for i := range names {
		names[i] = "job-" + string(rune('a'+i))
		concl := "failure"
		if i%3 == 0 {
			concl = "success"
		}
		target[i] = JobConclusion{Name: names[i], Conclusion: concl}
	}

	attempts := make([]Attempt, nAttempts)
	for a := range attempts {
		jobs := make([]JobConclusion, nJobs)
		for i := range names {
			concl := "failure"
			if (a+i)%2 == 0 {
				concl = "success"
			}
			jobs[i] = JobConclusion{Name: names[i], Conclusion: concl}
		}
		attempts[a] = Attempt{Number: a + 1, Jobs: jobs}
	}

	siblings := make([]SiblingRun, nSiblings)
	for s := range siblings {
		jobs := make([]JobConclusion, nJobs)
		for i := range names {
			concl := "failure"
			if (s+i)%2 == 1 {
				concl = "success"
			}
			jobs[i] = JobConclusion{Name: names[i], Conclusion: concl}
		}
		// Half the siblings share the target's workflow id (1), half a different
		// one (2), so Decide's Tier-B gate is exercised both ways.
		wfid := int64(1)
		if s%2 == 0 {
			wfid = 2
		}
		siblings[s] = SiblingRun{ID: int64(s + 1), WorkflowID: wfid, Event: event, Jobs: jobs}
	}

	return Evidence{
		RunID: 1, HeadSHA: "sha", WorkflowID: 1, Workflow: workflow, Event: event, RunAttempt: nAttempts + 1,
		TargetJobs:    target,
		PriorAttempts: attempts,
		Siblings:      siblings,
		Branch:        BranchHistory{Branch: "main", Runs: runs, Failures: failures},
	}
}

// FuzzDecide asserts the invariants the verdict must never violate, whatever the
// gathered evidence looks like.
func FuzzDecide(f *testing.F) {
	f.Add(1, 1, 1, 20, 3, "CI", "push")
	f.Add(0, 0, 0, 0, 0, "", "")
	f.Add(5, 3, 4, 50, 10, "CI", "pull_request")
	f.Add(8, 6, 6, 100, 100, "CI", "push")
	f.Fuzz(func(t *testing.T, nJobs, nAttempts, nSiblings, branchRuns, branchFailures int, workflow, event string) {
		ev := fuzzEvidence(nJobs, nAttempts, nSiblings, branchRuns, branchFailures, workflow, event)
		v := Decide(ev) // must never panic

		// The honesty gate: likely_flaky IFF there is at least one piece of evidence.
		if (len(v.Evidence) > 0) != (v.Verdict == VerdictLikelyFlaky) {
			t.Fatalf("honesty gate violated: evidence=%d verdict=%q", len(v.Evidence), v.Verdict)
		}
		// Confidence tracks the verdict exactly.
		wantHigh := v.Verdict == VerdictLikelyFlaky
		if (v.Confidence == "high") != wantHigh {
			t.Fatalf("confidence %q does not match verdict %q", v.Confidence, v.Verdict)
		}
		// runs_examined accounting is exact.
		if want := 1 + len(ev.PriorAttempts) + len(ev.Siblings) + ev.Branch.Runs; v.RunsExamined != want {
			t.Fatalf("runs_examined = %d, want %d", v.RunsExamined, want)
		}
		// base_rate, when present, is a real probability.
		if v.BaseRate != nil && (v.BaseRate.Rate < 0 || v.BaseRate.Rate > 1) {
			t.Fatalf("base_rate.rate = %v, out of [0,1]", v.BaseRate.Rate)
		}
		// failing_jobs always renders as an array.
		if v.FailingJobs == nil {
			t.Fatalf("failing_jobs must be non-nil")
		}
		// Every evidence must name a job that is actually failing on the target.
		failing := map[string]bool{}
		for _, j := range ev.TargetJobs {
			if j.Conclusion == "failure" {
				failing[j.Name] = true
			}
		}
		for _, e := range v.Evidence {
			if !failing[e.JobName] {
				t.Fatalf("evidence cites non-failing job %q", e.JobName)
			}
		}
	})
}
