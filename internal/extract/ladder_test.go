package extract

import "testing"

func TestSeverity_ranksBySeverity(t *testing.T) {
	panicLine := "panic: runtime error: invalid memory address"
	failLine := "--- FAIL: TestBudget (0.00s)"
	errLine := "error: something went wrong"
	warnLine := "warning: this API is deprecated"
	plainLine := "==> running the build now"

	// crash/fatal outranks a test failure outranks a plain error outranks a
	// warning outranks a non-matching line.
	if Severity(panicLine) <= Severity(failLine) {
		t.Errorf("panic (%d) should outrank FAIL (%d)", Severity(panicLine), Severity(failLine))
	}
	if Severity(failLine) <= Severity(errLine) {
		t.Errorf("FAIL (%d) should outrank error (%d)", Severity(failLine), Severity(errLine))
	}
	if Severity(errLine) <= Severity(warnLine) {
		t.Errorf("error (%d) should outrank warning (%d)", Severity(errLine), Severity(warnLine))
	}
	if Severity(warnLine) <= Severity(plainLine) {
		t.Errorf("warning (%d) should outrank plain (%d)", Severity(warnLine), Severity(plainLine))
	}
	if Severity(plainLine) != 0 {
		t.Errorf("plain line should score 0, got %d", Severity(plainLine))
	}
}

func TestSeverity_knownErrorForms(t *testing.T) {
	// Forms the memo calls out as the real signal in CI logs; each must score
	// above zero so the budget keeps them.
	matched := []string{
		"##[error]Process completed with exit code 1.",
		"--- FAIL: TestFoo/case_2 (0.01s)",
		"panic: assignment to entry in nil map",
		"FAILED tests/test_login.py::test_ok - AssertionError",
		"Error: Cannot find module 'react'",
		"fatal: not a git repository",
		"src/main.go:42: undefined: doThing",
		"✘ 3 tests failed",
		"go: cannot find main module",
	}
	for _, line := range matched {
		if Severity(line) == 0 {
			t.Errorf("expected a nonzero severity for %q", line)
		}
	}
}

func TestSeverity_ignoresBenignLines(t *testing.T) {
	benign := []string{
		"Downloading dependencies...",
		"Run actions/checkout@v4",
		"go: downloading github.com/spf13/cobra v1.10.2",
		"ok  \tgithub.com/akira-toriyama/cifail/internal/extract\t0.2s",
	}
	for _, line := range benign {
		if Severity(line) != 0 {
			t.Errorf("expected zero severity for benign line %q, got %d", line, Severity(line))
		}
	}
}
