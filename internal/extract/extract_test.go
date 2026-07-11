package extract

import (
	"fmt"
	"strings"
	"testing"
)

// linesOf flattens the excerpts of a step output back into their kept lines,
// in emission order, for assertions.
func linesOf(o StepOutput) []string {
	var got []string
	for _, e := range o.Excerpts {
		got = append(got, e.Lines...)
	}
	return got
}

func contains(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func TestExtractStep_smallLogKeptWhole(t *testing.T) {
	log := "line one\nline two\nerror: boom\nline four\n"
	out := ExtractStep(StepInput{Number: 1, Name: "test", Log: log}, 8192, Default())
	if out.OmittedLines != 0 {
		t.Fatalf("small log should be kept whole, omitted=%d", out.OmittedLines)
	}
	got := linesOf(out)
	if len(got) != 4 {
		t.Fatalf("want 4 lines, got %d: %v", len(got), got)
	}
	if !contains(got, "error: boom") {
		t.Errorf("kept lines lost the error line: %v", got)
	}
}

func TestExtractStep_keepsErrorOverFiller(t *testing.T) {
	// A long log of filler with a single error buried in the middle. With a
	// tight budget, the error line (and its context) must survive while most
	// filler is dropped.
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "filler line number %d doing routine work\n", i)
	}
	b.WriteString("panic: nil pointer dereference at main.go:42\n")
	for i := 200; i < 400; i++ {
		fmt.Fprintf(&b, "more filler line %d\n", i)
	}
	out := ExtractStep(StepInput{Number: 1, Name: "test", Log: b.String()}, 512, Default())

	got := linesOf(out)
	if !contains(got, "panic: nil pointer dereference") {
		t.Fatalf("budgeted output dropped the panic line: %v", got)
	}
	if out.OmittedLines == 0 {
		t.Errorf("expected omissions with a 512-byte budget over ~400 lines")
	}
	if out.UsedBytes > 512 {
		t.Errorf("used %d bytes over the 512 budget", out.UsedBytes)
	}
}

func TestExtractStep_tailBias(t *testing.T) {
	// No error lines at all: the extractor should still anchor head and tail so
	// the caller sees where the step started and, crucially, where it ended.
	var b strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "step output line %d\n", i)
	}
	out := ExtractStep(StepInput{Number: 1, Name: "build", Log: b.String()}, 400, Default())
	got := linesOf(out)
	if !contains(got, "step output line 0") {
		t.Errorf("expected the head to be anchored: %v", got)
	}
	if !contains(got, "step output line 299") {
		t.Errorf("expected the tail (last line) to be anchored: %v", got)
	}
}

func TestExtractStep_stripsTimestamps(t *testing.T) {
	log := "2026-07-03T00:00:00.1234567Z Starting the build\n" +
		"2026-07-03T00:00:01.7654321Z error: it broke\n"
	out := ExtractStep(StepInput{Number: 1, Name: "s", Log: log}, 8192, Default())
	got := linesOf(out)
	for _, l := range got {
		if strings.HasPrefix(l, "2026-07-03T") {
			t.Errorf("timestamp was not stripped: %q", l)
		}
	}
	if !contains(got, "error: it broke") {
		t.Errorf("content lost after stripping: %v", got)
	}
}

func TestExtractStep_startLinesAre1Based(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	out := ExtractStep(StepInput{Number: 1, Name: "s", Log: b.String()}, 60, Default())
	if len(out.Excerpts) == 0 {
		t.Fatal("expected at least one excerpt")
	}
	// The first excerpt is the head, so it must start at line 1.
	if out.Excerpts[0].StartLine != 1 {
		t.Errorf("first excerpt should start at line 1, got %d", out.Excerpts[0].StartLine)
	}
	// Every excerpt's declared start line must be sane.
	for _, e := range out.Excerpts {
		if e.StartLine < 1 {
			t.Errorf("excerpt start line must be >= 1, got %d", e.StartLine)
		}
	}
}

func TestExtractStep_emptyLogEmitsEmptyExcerpts(t *testing.T) {
	// A missing per-step archive leaves Log=="" (collect.go). Excerpts must be a
	// non-nil empty slice so model.FailedStep.Excerpts (no omitempty) serializes as
	// [] not null — an agent piping `.excerpts[]` must never hit a null.
	out := ExtractStep(StepInput{Number: 1, Name: "s", Log: ""}, 8192, Default())
	if out.Excerpts == nil {
		t.Errorf("empty log left Excerpts nil; want non-nil empty slice")
	}
}

func TestExtractStep_zeroBudgetEmitsEmptyExcerpts(t *testing.T) {
	// When an earlier step is budgeted last and the budget is already exhausted,
	// nothing survives and compose() keeps no blocks. Excerpts must still be a
	// non-nil empty slice, for the same JSON contract as above.
	out := ExtractStep(StepInput{Number: 1, Name: "s", Log: "a\nb\nc\n"}, 0, Default())
	if len(out.Excerpts) != 0 {
		t.Fatalf("zero budget should keep nothing, got %d excerpts", len(out.Excerpts))
	}
	if out.Excerpts == nil {
		t.Errorf("zero-budget step left Excerpts nil; want non-nil empty slice")
	}
}

func TestExtract_globalBudgetTailStepFirst(t *testing.T) {
	// Two failed steps; the LAST one holds the real error. Under a global budget
	// too small for both in full, the last step's error must survive.
	var early, late strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&early, "early step chatter %d\n", i)
		fmt.Fprintf(&late, "late step chatter %d\n", i)
	}
	late.WriteString("--- FAIL: TestCrucial (0.00s)\n")
	steps := []StepInput{
		{Number: 3, Name: "early", Log: early.String()},
		{Number: 7, Name: "late", Log: late.String()},
	}
	outs, budget := Extract(steps, Config{BudgetBytes: 600, Context: 3, Head: 6, Tail: 8, Floor: 2})
	if len(outs) != 2 {
		t.Fatalf("want 2 step outputs, got %d", len(outs))
	}
	// Output order preserved (early first).
	if outs[0].Number != 3 || outs[1].Number != 7 {
		t.Errorf("step order not preserved: %d, %d", outs[0].Number, outs[1].Number)
	}
	if !contains(linesOf(outs[1]), "--- FAIL: TestCrucial") {
		t.Errorf("the last step's failure was starved out of the budget")
	}
	if budget.UsedBytes > 600 {
		t.Errorf("global budget exceeded: %d > 600", budget.UsedBytes)
	}
	if budget.LimitBytes != 600 {
		t.Errorf("budget.LimitBytes = %d, want 600", budget.LimitBytes)
	}
}
