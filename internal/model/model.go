// Package model holds cifail's output shapes — the JSON an agent consumes. It
// is dependency-free (only the standard library) so every layer can share it:
// gh populates the run/job metadata, extract fills the per-step excerpts, and
// cli renders the whole Result.
package model

// Result is the top-level JSON document cifail prints on a successful run.
type Result struct {
	Run    Run    `json:"run"`
	Jobs   []Job  `json:"jobs"`
	Budget Budget `json:"budget"`
	// Note carries a human hint for edge cases the excerpts alone don't explain
	// — most importantly a failed run with zero jobs (a workflow-file syntax
	// error), where there are no step logs and the caller must open html_url.
	Note string `json:"note,omitempty"`
}

// Run identifies the workflow run whose failure cifail extracted.
type Run struct {
	ID         int64  `json:"id"`
	Name       string `json:"name,omitempty"`
	Status     string `json:"status"`     // e.g. completed
	Conclusion string `json:"conclusion"` // e.g. failure
	Event      string `json:"event,omitempty"`
	HeadBranch string `json:"head_branch,omitempty"`
	HeadSHA    string `json:"head_sha"`
	Attempt    int    `json:"run_attempt,omitempty"`
	HTMLURL    string `json:"html_url"`
}

// Job is one failing job in the run, carrying only its failed steps.
type Job struct {
	ID          int64        `json:"id"`
	Name        string       `json:"name"`
	Conclusion  string       `json:"conclusion"`
	HTMLURL     string       `json:"html_url,omitempty"`
	FailedSteps []FailedStep `json:"failed_steps"`
}

// FailedStep is a step whose conclusion was failure, with its annotations and
// the budgeted excerpts of its log.
type FailedStep struct {
	Number      int          `json:"number"`
	Name        string       `json:"name"`
	Annotations []Annotation `json:"annotations,omitempty"`
	Excerpts    []Excerpt    `json:"excerpts"`
	// OmittedLines counts log lines dropped from this step to fit the budget;
	// 0 means the whole step log is present.
	OmittedLines int `json:"omitted_lines,omitempty"`
}

// Annotation is a GitHub Checks annotation merged onto a failed step. These are
// free (a tiny API call) but sparse — present only when a problem matcher fired
// — so they supplement, never replace, the extracted excerpts.
type Annotation struct {
	Level     string `json:"level"` // failure | warning | notice
	Message   string `json:"message"`
	Path      string `json:"path,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Title     string `json:"title,omitempty"`
}

// Excerpt is one contiguous block of kept log lines. Blocks are emitted in log
// order; a gap between one block's line range and the next means lines were
// omitted there (see FailedStep.OmittedLines for the total).
type Excerpt struct {
	// StartLine is the 1-based line number in the step log where Lines begins,
	// so an agent can point a human at the exact spot in the full log.
	StartLine int `json:"start_line"`
	// Reason marks why this block survived the budget: "full", "head", "tail",
	// or "match" (an error line and its context).
	Reason string   `json:"reason"`
	Lines  []string `json:"lines"`
}

// Budget reports how the byte budget was spent, so the caller knows whether the
// output is complete or pared.
type Budget struct {
	LimitBytes   int `json:"limit_bytes"`
	UsedBytes    int `json:"used_bytes"`
	OmittedLines int `json:"omitted_lines"` // total across all steps
}
