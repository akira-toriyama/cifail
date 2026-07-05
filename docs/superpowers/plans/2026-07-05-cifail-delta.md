# `cifail delta` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `cifail delta` subcommand that reports, in one bounded JSON document, what changed between a failing GitHub Actions run and the last green run of the same workflow+branch — commits, lockfiles, workflow edits, resolved action SHAs, runner image.

**Architecture:** Mirrors the `flake` vertical exactly: `internal/cli/delta.go` (cobra adapter + narrow `deltaProber` interface + `buildDeltaEvidence` holding ALL IO) → `internal/delta` (pure: parsers, classification, budget, `Build(ev) → model.DeltaReport`) → `internal/gh` additions (`workflow_id` decode, `LastGreenRun`, `CompareCommits`, `RunSummary.HeadSHA`) → `internal/model/delta.go` (dep-free JSON shapes).

**Tech Stack:** Go (go.mod floor 1.25.0, toolchain go1.26.4), spf13/cobra (the ONLY dependency — add nothing), stdlib testing only.

**Spec:** `docs/superpowers/specs/2026-07-05-cifail-delta-design.md` (same repo). Two deliberate refinements vs the spec, to amend in the spec during Task 9: (1) no `ExitCode` func in `internal/delta` — a produced report always exits 0, there is no verdict enum to guard, so `runDelta` returns nil after printing; (2) optional blocks `commit_range`/`environment` are *omitted* (pointer + `omitempty`, the house convention) rather than rendered as `null`; only `last_green` renders an explicit `null` because it is the load-bearing field agents branch on.

## Global Constraints

- Exit-code contract is FROZEN: 0 produced report, 1 = `ResolveRun` soft miss (target not red/found), 2 usage, 3 API/IO, 130 interrupted-silent. No new codes, no per-verdict codes.
- ALL GitHub IO lives in `internal/gh`. `internal/delta` imports ONLY stdlib + `internal/model`. `internal/model` imports nothing internal.
- Every exported gh method takes `ctx` first; HTTP via the existing `getJSON`/`getRaw`; non-200s route through `statusError`.
- cli RunE order: validate flags with `core.Usagef` BEFORE any IO → `ctx := cmd.Context()` → getwd → `gh.ResolveRepo` → `gh.NewClient` → gh calls each wrapped in `interruptOr(ctx, err)` → pure compute → `printPretty`/`printCompact`.
- stdout is pure JSON only via the package `out` writer; never print errors yourself.
- Tests: stdlib only (NO testify, NO golden files). Table tests with rationale comments; httptest for gh; in-memory fakes for cli; fuzz for pure invariants.
- JSON: snake_case tags; `omitempty` on optional scalars; `*T` + `omitempty` for optional blocks; semantically always-present arrays initialized non-nil (render `[]` not `null`).
- Commits: gitmoji + Conventional Commits, English subject. Each task commits with the message given in its final step.
- Doc comments carry the WHY. Match surrounding comment density.

---

### Task 1: gh — carry `workflow_id` on runs and `head_sha` on run summaries

**Files:**
- Modify: `internal/model/model.go` (Run struct, ~line 19)
- Modify: `internal/gh/resolve.go` (apiRun ~line 23, toRun ~line 99)
- Modify: `internal/gh/runs.go` (RunSummary ~line 13, both mappings)
- Test: `internal/gh/delta_io_test.go` (new file)

**Interfaces:**
- Consumes: existing `apiRun`, `toRun`, `RunSummary`, `BranchRuns`, `RunsForSHA`.
- Produces: `model.Run.WorkflowID int64` (json `workflow_id,omitempty`); `gh.RunSummary.HeadSHA string`. Task 2 needs both; Task 8 reads `run.WorkflowID`.

- [ ] **Step 1: Write the failing tests**

Create `internal/gh/delta_io_test.go`:

```go
package gh

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// delta needs the run's workflow identity to find "the last green run of the
// SAME workflow" — matching by display name is rename-fragile, so ResolveRun
// must carry workflow_id through.
func TestResolveRunCarriesWorkflowID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/actions/runs/42") {
			t.Errorf("path = %q, want .../actions/runs/42", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":42,"workflow_id":7,"status":"completed",
			"conclusion":"failure","head_branch":"main","head_sha":"abc","html_url":"h"}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	run, err := c.ResolveRun(context.Background(), Target{RunID: 42})
	if err != nil {
		t.Fatalf("ResolveRun: %v", err)
	}
	if run.WorkflowID != 7 {
		t.Errorf("WorkflowID = %d, want 7", run.WorkflowID)
	}
}

// delta diffs the green run's commit against the failing run's, so run
// summaries must keep the head sha the API already sends.
func TestBranchRunsCarryHeadSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":1,"workflow_runs":[
			{"id":1,"status":"completed","conclusion":"success","head_sha":"def456"}]}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	runs, _, err := c.BranchRuns(context.Background(), "main", 5)
	if err != nil {
		t.Fatalf("BranchRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].HeadSHA != "def456" {
		t.Fatalf("runs = %+v, want one run with HeadSHA def456", runs)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/gh -run 'TestResolveRunCarriesWorkflowID|TestBranchRunsCarryHeadSHA' -v`
Expected: compile FAIL — `run.WorkflowID undefined` and `runs[0].HeadSHA undefined`.

- [ ] **Step 3: Implement**

In `internal/model/model.go`, add to `Run` (after `ID`):

```go
	// WorkflowID identifies the workflow this run belongs to — `delta` uses it
	// to find the last green run of the SAME workflow (display names can be
	// renamed; the id cannot).
	WorkflowID int64 `json:"workflow_id,omitempty"`
```

In `internal/gh/resolve.go`, add to `apiRun` (after `ID`):

```go
	WorkflowID   int64     `json:"workflow_id"`
```

and to `toRun`:

```go
		WorkflowID: r.WorkflowID,
```

In `internal/gh/runs.go`, replace the `RunSummary` struct and comment with:

```go
// RunSummary is a workflow run's status snapshot: enough for `wait` to poll for
// completion and compute elapsed from StartedAt, and for `delta` to diff the
// run's head commit (HeadSHA) against another run's.
type RunSummary struct {
	ID         int64
	Name       string
	Status     string // queued | in_progress | completed
	Conclusion string
	Event      string
	HTMLURL    string
	StartedAt  time.Time
	HeadSHA    string
}
```

and add `HeadSHA: r.HeadSHA,` to BOTH mappings (in `BranchRuns` and `RunsForSHA`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/gh ./internal/model ./internal/cli -count=1`
Expected: PASS (existing tests must stay green — the new fields are additive).

- [ ] **Step 5: Commit**

```bash
git add internal/model/model.go internal/gh/resolve.go internal/gh/runs.go internal/gh/delta_io_test.go
git commit -m ":sparkles: feat(gh): carry workflow_id on runs and head_sha on run summaries"
```

---

### Task 2: gh — `LastGreenRun`

**Files:**
- Create: `internal/gh/green.go`
- Test: `internal/gh/delta_io_test.go` (append)

**Interfaces:**
- Consumes: `apiRunList`, `getJSON`, `repoPath`, `RunSummary` (with `HeadSHA` from Task 1).
- Produces: `func (c *Client) LastGreenRun(ctx context.Context, workflowID int64, branch string) (RunSummary, bool, error)` — `(zero, false, nil)` when no green run exists. Task 8's prober declares this signature.

- [ ] **Step 1: Write the failing tests**

Append to `internal/gh/delta_io_test.go`:

```go
// LastGreenRun asks the workflow-scoped runs endpoint so the server filters by
// workflow AND status — one call, no client-side window that could miss older
// greens.
func TestLastGreenRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/actions/workflows/7/runs") {
			t.Errorf("path = %q, want .../actions/workflows/7/runs", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("branch") != "main" || q.Get("status") != "success" || q.Get("per_page") != "1" {
			t.Errorf("query = %v, want branch=main status=success per_page=1", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":9,"workflow_runs":[
			{"id":120,"status":"completed","conclusion":"success","head_sha":"def","html_url":"g"}]}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	run, ok, err := c.LastGreenRun(context.Background(), 7, "main")
	if err != nil {
		t.Fatalf("LastGreenRun: %v", err)
	}
	if !ok || run.ID != 120 || run.HeadSHA != "def" {
		t.Fatalf("got ok=%v run=%+v, want ok with ID 120 / HeadSHA def", ok, run)
	}
}

// A branch with no green history is a legitimate degrade for delta (report
// with last_green: null, exit 0) — so "none found" must be ok=false, NOT an error.
func TestLastGreenRunNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":0,"workflow_runs":[]}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	_, ok, err := c.LastGreenRun(context.Background(), 7, "main")
	if err != nil {
		t.Fatalf("LastGreenRun: %v", err)
	}
	if ok {
		t.Error("ok = true, want false for an empty run list")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/gh -run TestLastGreenRun -v`
Expected: compile FAIL — `c.LastGreenRun undefined`.

- [ ] **Step 3: Implement**

Create `internal/gh/green.go`:

```go
package gh

import (
	"context"
	"fmt"
	"net/url"
)

// LastGreenRun returns the newest successful run of the given workflow on the
// branch — the baseline `delta` diffs a failing run against. The bool reports
// whether one exists: a branch with no green history is a legitimate degrade
// (delta still produces a report), not an error. The workflow-scoped endpoint
// filters server-side, so one call suffices and no client window can miss an
// older green.
func (c *Client) LastGreenRun(ctx context.Context, workflowID int64, branch string) (RunSummary, bool, error) {
	v := url.Values{"branch": {branch}, "status": {"success"}, "per_page": {"1"}}
	var list apiRunList
	path := c.repoPath(fmt.Sprintf("/actions/workflows/%d/runs?%s", workflowID, v.Encode()))
	if err := c.getJSON(ctx, path, &list); err != nil {
		return RunSummary{}, false, err
	}
	if len(list.Runs) == 0 {
		return RunSummary{}, false, nil
	}
	r := list.Runs[0]
	return RunSummary{
		ID: r.ID, Name: r.Name, Status: r.Status, Conclusion: r.Conclusion,
		Event: r.Event, HTMLURL: r.HTMLURL, StartedAt: r.RunStartedAt, HeadSHA: r.HeadSHA,
	}, true, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/gh -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/gh/green.go internal/gh/delta_io_test.go
git commit -m ":sparkles: feat(gh): add LastGreenRun — newest green run of a workflow on a branch"
```

---

### Task 3: gh — `CompareCommits`

**Files:**
- Create: `internal/gh/compare.go`
- Test: `internal/gh/delta_io_test.go` (append)

**Interfaces:**
- Consumes: `getJSON`, `repoPath`, `statusError` (via getJSON).
- Produces (Task 8's prober + Task 8's conversion to `delta.Comparison` rely on these exact shapes):

```go
type Comparison struct {
	AheadBy      int
	BehindBy     int
	TotalCommits int
	Files        []ComparedFile
	Capped       bool
}
type ComparedFile struct {
	Path      string
	Additions int
	Deletions int
	Patch     string
}
func (c *Client) CompareCommits(ctx context.Context, base, head string) (Comparison, error)
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/gh/delta_io_test.go`:

```go
// CompareCommits is delta's commit_range source: base = green sha, head =
// failing sha. behind_by > 0 is the rebase/force-push hidden-delta signal.
func TestCompareCommits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/compare/def...abc") {
			t.Errorf("path = %q, want .../compare/def...abc", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ahead_by":3,"behind_by":1,"total_commits":3,"files":[
			{"filename":"go.sum","additions":14,"deletions":9,"patch":"@@ -1 +1 @@"},
			{"filename":"src/api/x.go","additions":2,"deletions":0}]}`)
	}))
	defer srv.Close()

	c := &Client{Owner: "o", Repo: "r", token: "t", http: srv.Client(), base: srv.URL}
	cmp, err := c.CompareCommits(context.Background(), "def", "abc")
	if err != nil {
		t.Fatalf("CompareCommits: %v", err)
	}
	if cmp.AheadBy != 3 || cmp.BehindBy != 1 || cmp.TotalCommits != 3 {
		t.Errorf("counts = %+v, want ahead 3 / behind 1 / commits 3", cmp)
	}
	if len(cmp.Files) != 2 || cmp.Files[0].Path != "go.sum" || cmp.Files[0].Additions != 14 || cmp.Files[0].Patch == "" {
		t.Errorf("files = %+v, want go.sum first with stats and patch", cmp.Files)
	}
	if cmp.Capped {
		t.Error("Capped = true, want false for 2 files")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/gh -run TestCompareCommits -v`
Expected: compile FAIL — `c.CompareCommits undefined`.

- [ ] **Step 3: Implement**

Create `internal/gh/compare.go`:

```go
package gh

import (
	"context"
	"fmt"
	"net/url"
)

// compareFilesCap is the compare API's hard limit on listed files; hitting it
// means the file survey may be incomplete and must be reported as such.
const compareFilesCap = 300

// Comparison summarises base...head: how far head is ahead/behind base and
// which files changed. BehindBy > 0 means head's history LOST commits base had
// (a rebase / force-push signal). Capped reports the compare API's 300-file
// listing limit so a truncated survey isn't mistaken for a full one.
type Comparison struct {
	AheadBy      int
	BehindBy     int
	TotalCommits int
	Files        []ComparedFile
	Capped       bool
}

// ComparedFile is one changed file with its diff stat and (when the API sends
// one — it omits patches for large/binary files) its unified patch text.
type ComparedFile struct {
	Path      string
	Additions int
	Deletions int
	Patch     string
}

type apiCompareFile struct {
	Filename  string `json:"filename"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"`
}

type apiComparison struct {
	AheadBy      int              `json:"ahead_by"`
	BehindBy     int              `json:"behind_by"`
	TotalCommits int              `json:"total_commits"`
	Files        []apiCompareFile `json:"files"`
}

// CompareCommits fetches the two-dot comparison base...head.
func (c *Client) CompareCommits(ctx context.Context, base, head string) (Comparison, error) {
	path := c.repoPath(fmt.Sprintf("/compare/%s...%s", url.PathEscape(base), url.PathEscape(head)))
	var cmp apiComparison
	if err := c.getJSON(ctx, path, &cmp); err != nil {
		return Comparison{}, err
	}
	out := Comparison{
		AheadBy: cmp.AheadBy, BehindBy: cmp.BehindBy, TotalCommits: cmp.TotalCommits,
		Files:  make([]ComparedFile, 0, len(cmp.Files)),
		Capped: len(cmp.Files) >= compareFilesCap,
	}
	for _, f := range cmp.Files {
		out.Files = append(out.Files, ComparedFile{
			Path: f.Filename, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch,
		})
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/gh -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/gh/compare.go internal/gh/delta_io_test.go
git commit -m ":sparkles: feat(gh): add CompareCommits over the compare API"
```

---

### Task 4: model — delta report JSON shapes

**Files:**
- Create: `internal/model/delta.go`
- Test: `internal/model/delta_test.go`

**Interfaces:**
- Consumes: nothing (model is dependency-free).
- Produces: every type below, exactly as written — Tasks 5–8 construct them.

- [ ] **Step 1: Write the failing tests**

Create `internal/model/delta_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/model -run TestDelta -v`
Expected: compile FAIL — `undefined: DeltaReport`.

- [ ] **Step 3: Implement**

Create `internal/model/delta.go`:

```go
package model

// DeltaReport is the JSON `cifail delta` prints: the input delta between a
// failing run and the last green run of the same workflow+branch. LastGreen is
// the load-bearing field an agent branches on first, so it renders an explicit
// null when no green baseline exists; optional context blocks (CommitRange,
// Environment) are omitted instead. Any produced report exits 0 — degraded
// reports carry a Note saying what is missing and why.
type DeltaReport struct {
	Failing    DeltaRun  `json:"failing"`
	LastGreen  *DeltaRun `json:"last_green"` // explicit null when absent
	SameCommit bool      `json:"same_commit"`
	// CommitRange is omitted when the runs share a commit (git has no answer —
	// see SameCommit) or when no comparison could be made (Note explains).
	CommitRange *DeltaCommitRange `json:"commit_range,omitempty"`
	// Environment is omitted when either run's logs are unavailable (expired
	// retention — Note explains); it carries the drift git diffs cannot see.
	Environment *DeltaEnvironment `json:"environment,omitempty"`
	Jobs        DeltaJobs         `json:"jobs"`
	Budget      DeltaBudget       `json:"budget"`
	Note        string            `json:"note,omitempty"`
}

// DeltaRun identifies one side of the comparison.
type DeltaRun struct {
	Run     int64  `json:"run"`
	SHA     string `json:"sha"`
	Attempt int    `json:"attempt,omitempty"`
	// AgeDays is how many days before "now" the green run started — how stale
	// the baseline is. Only set on last_green.
	AgeDays int    `json:"age_days,omitempty"`
	URL     string `json:"url,omitempty"`
}

// DeltaCommitRange summarises green...failing: commit counts and the changed
// files classified into agent-actionable buckets. The arrays are always
// present ([] not null) — an empty bucket is a finding.
type DeltaCommitRange struct {
	AheadBy  int `json:"ahead_by"`
	BehindBy int `json:"behind_by"` // >0: rebase/force-push hid history
	// FilesChanged is the number of files LISTED by the compare API; when
	// FilesCapped is true the true count may be higher (API caps at 300).
	FilesChanged    int                   `json:"files_changed"`
	FilesCapped     bool                  `json:"files_capped,omitempty"`
	TopDirs         []string              `json:"top_dirs"` // "dir (N)", largest first
	Lockfiles       []DeltaLockfile       `json:"lockfiles"`
	WorkflowChanges []DeltaWorkflowChange `json:"workflow_changes"`
}

// DeltaLockfile is a changed dependency lockfile — v1 detects WHICH moved and
// by how many lines; it does not parse version-level changes.
type DeltaLockfile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// DeltaWorkflowChange is a changed workflow file, with any `uses:` refs that
// moved in its patch ("owner/repo old→new").
type DeltaWorkflowChange struct {
	Path        string   `json:"path"`
	UsesChanged []string `json:"uses_changed"`
}

// DeltaEnvironment is the drift no text diff can catch: what the runs' 'Set up
// job' logs recorded as the RESOLVED action SHAs and runner image.
type DeltaEnvironment struct {
	Actions []DeltaActionDrift `json:"actions"`
	Runner  *DeltaRunnerDrift  `json:"runner,omitempty"`
}

// DeltaActionDrift compares one action ref's resolved SHA across the two runs.
// A missing side (the run didn't download that action) leaves its SHA empty
// and Drifted false — absence of proof, not proof of drift.
type DeltaActionDrift struct {
	Ref        string `json:"ref"` // as written in the workflow, e.g. "actions/checkout@v4"
	GreenSHA   string `json:"green_sha,omitempty"`
	FailingSHA string `json:"failing_sha,omitempty"`
	Drifted    bool   `json:"drifted"`
}

// DeltaRunnerDrift compares the runner image ("image/version") across the runs.
type DeltaRunnerDrift struct {
	Green   string `json:"green"`
	Failing string `json:"failing"`
	Drifted bool   `json:"drifted"`
}

// DeltaJobs maps the input delta to its observable effect: jobs failing now
// whose same-named job passed in the green run. Always present ([] not null).
type DeltaJobs struct {
	NewlyFailing []string `json:"newly_failing"`
}

// DeltaBudget reports how the byte budget was spent over the report's
// variable-length lists, so the caller knows whether the survey is complete.
type DeltaBudget struct {
	LimitBytes   int `json:"limit_bytes"`
	UsedBytes    int `json:"used_bytes"`
	OmittedItems int `json:"omitted_items"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/model -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/model/delta.go internal/model/delta_test.go
git commit -m ":sparkles: feat(model): add delta report JSON shapes"
```

---

### Task 5: delta — package skeleton + 'Set up job' parser

**Files:**
- Create: `internal/delta/delta.go` (package doc, Config, Evidence value types)
- Create: `internal/delta/setup.go`
- Test: `internal/delta/setup_test.go`

**Interfaces:**
- Consumes: nothing outside stdlib + `internal/model` (and model only from Task 7 onward).
- Produces (Tasks 7–8 rely on these exact names):

```go
const DefaultBudgetBytes = 4096
type Config struct{ BudgetBytes int }
func Default() Config
type RunMeta struct{ ID int64; SHA string; Attempt int; URL string; StartedAt time.Time }
type JobOutcome struct{ Name, Conclusion string }
type FileChange struct{ Path string; Additions, Deletions int; Patch string }
type Comparison struct{ AheadBy, BehindBy int; Files []FileChange; Capped bool }
type SetupLog struct{ Job, Text string }
type Evidence struct {
	Failing RunMeta; Green *RunMeta; Now time.Time
	Compare *Comparison; CompareNote string
	FailingSetup, GreenSetup []SetupLog; EnvNote string
	FailingJobs, GreenJobs []JobOutcome
}
type ActionResolution struct{ Ref, SHA string }
type Setup struct{ Actions []ActionResolution; Runner string }
func ParseSetup(text string) Setup
```

- [ ] **Step 1: Write the failing tests**

Create `internal/delta/setup_test.go`:

```go
package delta

import (
	"reflect"
	"testing"
)

// A realistic 'Set up job' step log: every archive line carries an RFC3339Nano
// timestamp prefix that must be stripped before matching.
const setupFixture = `2026-07-01T03:04:05.1234567Z Current runner version: '2.325.0'
2026-07-01T03:04:05.1234567Z ##[group]Operating System
2026-07-01T03:04:05.1234567Z Ubuntu
2026-07-01T03:04:05.1234567Z 24.04.2
2026-07-01T03:04:05.1234567Z LTS
2026-07-01T03:04:05.1234567Z ##[endgroup]
2026-07-01T03:04:05.1234567Z ##[group]Runner Image
2026-07-01T03:04:05.1234567Z Image: ubuntu-24.04
2026-07-01T03:04:05.1234567Z Version: 20250601.1.0
2026-07-01T03:04:05.1234567Z Included Software: https://example.test/sw
2026-07-01T03:04:05.1234567Z ##[endgroup]
2026-07-01T03:04:06.1234567Z Download action repository 'actions/checkout@v4' (SHA:11bd71901bbe5b1630ceea73d27597364c9af683)
2026-07-01T03:04:07.1234567Z Download action repository 'actions/setup-go@v5' (SHA:d35c59abb061a4a6fb18e82ac0862c26744d6ab5)
2026-07-01T03:04:08.1234567Z Complete job name: test`

func TestParseSetup(t *testing.T) {
	s := ParseSetup(setupFixture)
	wantActions := []ActionResolution{
		{Ref: "actions/checkout@v4", SHA: "11bd71901bbe5b1630ceea73d27597364c9af683"},
		{Ref: "actions/setup-go@v5", SHA: "d35c59abb061a4a6fb18e82ac0862c26744d6ab5"},
	}
	if !reflect.DeepEqual(s.Actions, wantActions) {
		t.Errorf("Actions = %+v, want %+v", s.Actions, wantActions)
	}
	if s.Runner != "ubuntu-24.04/20250601.1.0" {
		t.Errorf("Runner = %q, want ubuntu-24.04/20250601.1.0", s.Runner)
	}
}

// Version: must only count AFTER Image: — a stray Version: line earlier in the
// log (e.g. runner version) must not be mistaken for the image version.
func TestParseSetupVersionRequiresImageFirst(t *testing.T) {
	s := ParseSetup("2026-07-01T03:04:05Z Version: 9.9.9\n2026-07-01T03:04:05Z Image: ubuntu-22.04\n2026-07-01T03:04:05Z Version: 20250101.2.0")
	if s.Runner != "ubuntu-22.04/20250101.2.0" {
		t.Errorf("Runner = %q, want ubuntu-22.04/20250101.2.0", s.Runner)
	}
}

func TestParseSetupEmptyAndGarbage(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"empty", ""},
		{"no matches", "2026-07-01T03:04:05Z hello\nworld"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ParseSetup(tc.in)
			if len(s.Actions) != 0 || s.Runner != "" {
				t.Errorf("ParseSetup(%q) = %+v, want empty", tc.in, s)
			}
		})
	}
}

func TestDefault(t *testing.T) {
	if got := Default().BudgetBytes; got != DefaultBudgetBytes || got != 4096 {
		t.Errorf("Default().BudgetBytes = %d, want 4096", got)
	}
}

// The parser sees arbitrary log bytes (truncated archives, binary junk); it
// must never panic and never emit half-empty resolutions.
func FuzzParseSetup(f *testing.F) {
	f.Add(setupFixture)
	f.Add("")
	f.Add("2026-07-01T00:00:00Z Download action repository '' (SHA:)")
	f.Fuzz(func(t *testing.T, text string) {
		s := ParseSetup(text)
		for _, a := range s.Actions {
			if a.Ref == "" || a.SHA == "" {
				t.Fatalf("empty field in %+v", a)
			}
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/delta -v`
Expected: FAIL — package does not exist yet.

- [ ] **Step 3: Implement**

Create `internal/delta/delta.go`:

```go
// Package delta computes — purely and without any IO — the input delta between
// a failing workflow run and the last green run of the same workflow+branch:
// commit range, lockfile and workflow-file changes, resolved action SHAs, and
// runner image. The cli adapter gathers an Evidence snapshot (all IO lives
// there); Build reasons over it. Keep this package dependency-free (stdlib +
// model) so the report logic stays unit-testable with no network.
package delta

import "time"

// DefaultBudgetBytes bounds the report's variable-length lists. Smaller than
// root's 8 KiB log budget: delta's lists are structured summaries, not logs.
const DefaultBudgetBytes = 4096

// Config is the feature's knob namespace; the cli seeds its flag defaults from
// Default() so --help shows the real values.
type Config struct {
	BudgetBytes int
}

// Default returns the standard budget.
func Default() Config {
	return Config{BudgetBytes: DefaultBudgetBytes}
}

// RunMeta identifies one side of the comparison. StartedAt is used only for
// the green run's age; a zero value simply omits age_days.
type RunMeta struct {
	ID        int64
	SHA       string
	Attempt   int
	URL       string
	StartedAt time.Time
}

// JobOutcome is one job's terminal state. Job identity is the exact display
// name (the matrix identity), as everywhere in cifail.
type JobOutcome struct {
	Name       string
	Conclusion string
}

// FileChange is one changed file from the commit comparison.
type FileChange struct {
	Path      string
	Additions int
	Deletions int
	Patch     string
}

// Comparison is the green...failing commit comparison. Capped reports the
// compare API's file-listing limit was hit (survey may be incomplete).
type Comparison struct {
	AheadBy  int
	BehindBy int
	Files    []FileChange
	Capped   bool
}

// SetupLog is one job's 'Set up job' step text (or the head of its whole-job
// log when the per-step file is absent from the archive).
type SetupLog struct {
	Job  string
	Text string
}

// Evidence is the materialized snapshot Build reasons over — gathered by the
// cli adapter (all IO), decided here (no IO). Nil/empty optional fields mean
// the data was unavailable; the *Note fields say why, verbatim for the report.
type Evidence struct {
	Failing RunMeta
	Green   *RunMeta // nil: no green run of this workflow on the branch
	Now     time.Time

	Compare     *Comparison // nil: same commit, no green, or comparison degraded
	CompareNote string      // set when Compare degraded (e.g. force-push 404)

	FailingSetup []SetupLog
	GreenSetup   []SetupLog
	EnvNote      string // set when either side's logs were unavailable

	FailingJobs []JobOutcome
	GreenJobs   []JobOutcome
}
```

Create `internal/delta/setup.go`:

```go
package delta

import (
	"regexp"
	"strings"
)

// ActionResolution is one 'Download action repository' line: the ref as
// written in the workflow and the SHA it resolved to AT RUN TIME. Comparing
// these across runs catches a floating tag that moved — drift no text diff of
// the repo can see.
type ActionResolution struct {
	Ref string // e.g. "actions/checkout@v4"
	SHA string
}

// Setup is what ParseSetup extracts from one job's 'Set up job' log.
type Setup struct {
	Actions []ActionResolution
	Runner  string // "image/version" (e.g. "ubuntu-24.04/20250601.1.0"); "" if absent
}

var (
	// Every log-archive line starts with an RFC3339Nano timestamp.
	tsPrefix   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z\s?`)
	downloadRe = regexp.MustCompile(`^Download action repository '([^']+)' \(SHA:([0-9a-fA-F]+)\)`)
	imageRe    = regexp.MustCompile(`^\s*Image:\s*(\S+)`)
	versionRe  = regexp.MustCompile(`^\s*Version:\s*(\S+)`)
)

// ParseSetup scans a 'Set up job' step log (or a whole-job log whose head
// contains it) for resolved action SHAs and the runner image. The image
// version is only taken AFTER an Image: line, so unrelated Version: lines
// (e.g. the runner agent's) aren't mistaken for it.
func ParseSetup(text string) Setup {
	var s Setup
	image, version := "", ""
	for _, line := range strings.Split(text, "\n") {
		line = tsPrefix.ReplaceAllString(line, "")
		if m := downloadRe.FindStringSubmatch(line); m != nil {
			s.Actions = append(s.Actions, ActionResolution{Ref: m[1], SHA: m[2]})
			continue
		}
		if image == "" {
			if m := imageRe.FindStringSubmatch(line); m != nil {
				image = m[1]
			}
			continue
		}
		if version == "" {
			if m := versionRe.FindStringSubmatch(line); m != nil {
				version = m[1]
			}
		}
	}
	switch {
	case image != "" && version != "":
		s.Runner = image + "/" + version
	case image != "":
		s.Runner = image
	}
	return s
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/delta -count=1 -v`
Expected: PASS (all four tests)

- [ ] **Step 5: Commit**

```bash
git add internal/delta/delta.go internal/delta/setup.go internal/delta/setup_test.go
git commit -m ":sparkles: feat(delta): parse 'Set up job' logs — action SHAs and runner image"
```

---

### Task 6: delta — commit-range classification

**Files:**
- Create: `internal/delta/classify.go`
- Test: `internal/delta/classify_test.go`

**Interfaces:**
- Consumes: `FileChange`, `Comparison` (Task 5); `model.DeltaCommitRange` etc. (Task 4).
- Produces: `func buildCommitRange(cmp *Comparison) *model.DeltaCommitRange` (unexported; Task 7's `Build` calls it). Helpers `topDir`, `usesChanged`, `splitUses` (unexported).

- [ ] **Step 1: Write the failing tests**

Create `internal/delta/classify_test.go`:

```go
package delta

import (
	"reflect"
	"testing"
)

func TestBuildCommitRange(t *testing.T) {
	cmp := &Comparison{
		AheadBy: 3, BehindBy: 1, Capped: false,
		Files: []FileChange{
			{Path: "src/api/a.go", Additions: 5, Deletions: 1},
			{Path: "src/api/b.go", Additions: 2, Deletions: 0},
			{Path: "go.sum", Additions: 14, Deletions: 9},
			{Path: "README.md", Additions: 1, Deletions: 1},
			{Path: ".github/workflows/ci.yml", Additions: 1, Deletions: 1,
				Patch: "@@ -10 +10 @@\n-      - uses: actions/setup-node@v4\n+      - uses: actions/setup-node@v5"},
		},
	}
	cr := buildCommitRange(cmp)
	if cr.AheadBy != 3 || cr.BehindBy != 1 || cr.FilesChanged != 5 {
		t.Errorf("counts = %+v, want ahead 3 / behind 1 / files 5", cr)
	}
	// Largest dir first; ties by name ascending ("." sorts before "src/api");
	// root files under ".".
	wantDirs := []string{". (2)", "src/api (2)", ".github/workflows (1)"}
	if !reflect.DeepEqual(cr.TopDirs, wantDirs) {
		t.Errorf("TopDirs = %v, want %v", cr.TopDirs, wantDirs)
	}
	if len(cr.Lockfiles) != 1 || cr.Lockfiles[0].Path != "go.sum" || cr.Lockfiles[0].Additions != 14 {
		t.Errorf("Lockfiles = %+v, want go.sum +14", cr.Lockfiles)
	}
	if len(cr.WorkflowChanges) != 1 || cr.WorkflowChanges[0].Path != ".github/workflows/ci.yml" {
		t.Fatalf("WorkflowChanges = %+v, want ci.yml", cr.WorkflowChanges)
	}
	wantUses := []string{"actions/setup-node v4→v5"}
	if !reflect.DeepEqual(cr.WorkflowChanges[0].UsesChanged, wantUses) {
		t.Errorf("UsesChanged = %v, want %v", cr.WorkflowChanges[0].UsesChanged, wantUses)
	}
}

// A workflow patch whose uses: lines did not move (or only appear on one side)
// must yield an empty — but non-nil — uses_changed.
func TestUsesChangedNoPairs(t *testing.T) {
	got := usesChanged("@@ -1 +1 @@\n+      - uses: actions/cache@v4\n- name: x")
	if got == nil || len(got) != 0 {
		t.Errorf("usesChanged = %#v, want empty non-nil slice", got)
	}
}

// SHA-pinned refs with a trailing version comment must compare on the ref
// token only (the comment is not part of the ref).
func TestUsesChangedShaPinned(t *testing.T) {
	patch := "-        uses: actions/checkout@11bd719 # v4.2.2\n+        uses: actions/checkout@08c6903 # v5.0.0"
	want := []string{"actions/checkout 11bd719→08c6903"}
	if got := usesChanged(patch); !reflect.DeepEqual(got, want) {
		t.Errorf("usesChanged = %v, want %v", got, want)
	}
}

func TestTopDir(t *testing.T) {
	for in, want := range map[string]string{
		"main.go":               ".",
		"cmd/x.go":              "cmd",
		"internal/gh/client.go": "internal/gh",
		"a/b/c/d.go":            "a/b",
	} {
		if got := topDir(in); got != want {
			t.Errorf("topDir(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/delta -run 'TestBuildCommitRange|TestUsesChanged|TestTopDir' -v`
Expected: compile FAIL — `undefined: buildCommitRange`.

- [ ] **Step 3: Implement**

Create `internal/delta/classify.go`:

```go
package delta

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/akira-toriyama/cifail/internal/model"
)

// lockfileNames are dependency lockfiles worth calling out by name. v1 detects
// WHICH moved and by how many lines; version-level parsing is deliberately out.
var lockfileNames = map[string]bool{
	"go.sum": true, "go.mod": true,
	"package-lock.json": true, "pnpm-lock.yaml": true, "yarn.lock": true,
	"uv.lock": true, "Cargo.lock": true, "Gemfile.lock": true, "composer.lock": true,
}

var usesRe = regexp.MustCompile(`^[+-].*\buses:\s*([^\s#'"]+)`)

// buildCommitRange classifies the compared files into agent-actionable
// buckets. All arrays come back non-nil so they render as [].
func buildCommitRange(cmp *Comparison) *model.DeltaCommitRange {
	cr := &model.DeltaCommitRange{
		AheadBy: cmp.AheadBy, BehindBy: cmp.BehindBy,
		FilesChanged: len(cmp.Files), FilesCapped: cmp.Capped,
		TopDirs:         []string{},
		Lockfiles:       []model.DeltaLockfile{},
		WorkflowChanges: []model.DeltaWorkflowChange{},
	}
	counts := map[string]int{}
	var dirOrder []string
	for _, f := range cmp.Files {
		d := topDir(f.Path)
		if counts[d] == 0 {
			dirOrder = append(dirOrder, d)
		}
		counts[d]++
		if lockfileNames[f.Path[strings.LastIndex(f.Path, "/")+1:]] {
			cr.Lockfiles = append(cr.Lockfiles, model.DeltaLockfile{
				Path: f.Path, Additions: f.Additions, Deletions: f.Deletions,
			})
		}
		if strings.HasPrefix(f.Path, ".github/workflows/") {
			cr.WorkflowChanges = append(cr.WorkflowChanges, model.DeltaWorkflowChange{
				Path: f.Path, UsesChanged: usesChanged(f.Patch),
			})
		}
	}
	sort.SliceStable(dirOrder, func(i, j int) bool {
		if counts[dirOrder[i]] != counts[dirOrder[j]] {
			return counts[dirOrder[i]] > counts[dirOrder[j]]
		}
		return dirOrder[i] < dirOrder[j]
	})
	for _, d := range dirOrder {
		cr.TopDirs = append(cr.TopDirs, fmt.Sprintf("%s (%d)", d, counts[d]))
	}
	return cr
}

// topDir reduces a path to its depth-2 directory ("internal/gh"); files at the
// repo root fall under ".".
func topDir(path string) string {
	parts := strings.Split(path, "/")
	switch len(parts) {
	case 1:
		return "."
	case 2:
		return parts[0]
	default:
		return parts[0] + "/" + parts[1]
	}
}

// usesChanged pairs removed/added `uses:` refs in a workflow patch by action
// name and reports the ones that moved as "name old→new". Unpaired lines
// (added-only, removed-only) are not reported — they are visible in the file
// change itself and pairing them would guess.
func usesChanged(patch string) []string {
	removed, added := map[string]string{}, map[string]string{}
	var order []string
	for _, line := range strings.Split(patch, "\n") {
		m := usesRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name, ref := splitUses(m[1])
		if strings.HasPrefix(line, "-") {
			if _, ok := removed[name]; !ok {
				removed[name] = ref
			}
			continue
		}
		if _, ok := added[name]; !ok {
			added[name] = ref
			order = append(order, name)
		}
	}
	out := make([]string, 0)
	for _, name := range order {
		if old, ok := removed[name]; ok && old != added[name] {
			out = append(out, fmt.Sprintf("%s %s→%s", name, old, added[name]))
		}
	}
	return out
}

// splitUses splits "owner/repo@ref" into name and ref (ref may be empty for a
// local composite action path).
func splitUses(u string) (name, ref string) {
	if i := strings.LastIndex(u, "@"); i >= 0 {
		return u[:i], u[i+1:]
	}
	return u, ""
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/delta -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/delta/classify.go internal/delta/classify_test.go
git commit -m ":sparkles: feat(delta): classify commit-range files — top dirs, lockfiles, workflow uses"
```

---

### Task 7: delta — `Build`, environment drift, budget, fuzz

**Files:**
- Create: `internal/delta/build.go`
- Test: `internal/delta/build_test.go`
- Test: `internal/delta/fuzz_test.go`

**Interfaces:**
- Consumes: everything from Tasks 4–6.
- Produces: `func Build(ev Evidence, cfg Config) model.DeltaReport` — the ONE public entry point Task 8 calls. Unexported helpers: `buildEnvironment`, `mergeSetups`, `newlyFailing`, `applyBudget`, `budget`.

- [ ] **Step 1: Write the failing tests**

Create `internal/delta/build_test.go`:

```go
package delta

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func greenMeta(sha string, started time.Time) *RunMeta {
	return &RunMeta{ID: 120, SHA: sha, URL: "g", StartedAt: started}
}

var now = time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

// The flagship degrade: no green baseline still produces a report (exit 0)
// with an explicit null last_green and a note — never an error.
func TestBuildNoGreen(t *testing.T) {
	r := Build(Evidence{
		Failing: RunMeta{ID: 123, SHA: "abc", Attempt: 2, URL: "f"},
		Now:     now,
	}, Default())
	if r.LastGreen != nil {
		t.Errorf("LastGreen = %+v, want nil", r.LastGreen)
	}
	if !strings.Contains(r.Note, "no green run") {
		t.Errorf("Note = %q, want a no-green explanation", r.Note)
	}
	if r.CommitRange != nil || r.Environment != nil {
		t.Error("CommitRange/Environment should be absent with no baseline")
	}
	if r.Jobs.NewlyFailing == nil {
		t.Error("NewlyFailing must be non-nil ([])")
	}
}

// Zero-commit pivot: same sha means git has no answer — commit_range is
// omitted even if a Comparison was (wrongly) supplied, and the note points at
// environment drift.
func TestBuildSameCommitPivots(t *testing.T) {
	r := Build(Evidence{
		Failing: RunMeta{ID: 123, SHA: "abc"},
		Green:   greenMeta("abc", now.Add(-49*time.Hour)),
		Now:     now,
		Compare: &Comparison{AheadBy: 1},
	}, Default())
	if !r.SameCommit {
		t.Fatal("SameCommit = false, want true")
	}
	if r.CommitRange != nil {
		t.Error("CommitRange present on same commit, want omitted")
	}
	if !strings.Contains(r.Note, "environment") {
		t.Errorf("Note = %q, want a pivot to environment drift", r.Note)
	}
	if r.LastGreen == nil || r.LastGreen.AgeDays != 2 {
		t.Errorf("LastGreen = %+v, want age_days 2 (49h)", r.LastGreen)
	}
}

// Action drift: same ref, different resolved SHA across the runs. A ref seen
// on only one side must never count as drifted.
func TestBuildEnvironmentDrift(t *testing.T) {
	green := []SetupLog{{Job: "test", Text: "2026-07-01T00:00:00Z Download action repository 'actions/checkout@v4' (SHA:aaa1)\n2026-07-01T00:00:00Z Image: ubuntu-24.04\n2026-07-01T00:00:00Z Version: 20250601.1.0"}}
	failing := []SetupLog{{Job: "test", Text: "2026-07-02T00:00:00Z Download action repository 'actions/checkout@v4' (SHA:bbb2)\n2026-07-02T00:00:00Z Download action repository 'actions/cache@v4' (SHA:ccc3)\n2026-07-02T00:00:00Z Image: ubuntu-24.04\n2026-07-02T00:00:00Z Version: 20250620.2.0"}}
	r := Build(Evidence{
		Failing: RunMeta{ID: 123, SHA: "abc"}, Green: greenMeta("abc", now), Now: now,
		FailingSetup: failing, GreenSetup: green,
	}, Default())
	if r.Environment == nil {
		t.Fatal("Environment = nil, want drift report")
	}
	var checkout, cache int = -1, -1
	for i, a := range r.Environment.Actions {
		switch a.Ref {
		case "actions/checkout@v4":
			checkout = i
		case "actions/cache@v4":
			cache = i
		}
	}
	if checkout < 0 || !r.Environment.Actions[checkout].Drifted {
		t.Errorf("checkout@v4 should be drifted: %+v", r.Environment.Actions)
	}
	if cache < 0 || r.Environment.Actions[cache].Drifted {
		t.Errorf("cache@v4 (one side only) must not be drifted: %+v", r.Environment.Actions)
	}
	if r.Environment.Runner == nil || !r.Environment.Runner.Drifted {
		t.Errorf("Runner = %+v, want drifted image versions", r.Environment.Runner)
	}
}

// Expired logs degrade environment to absent, with the gatherer's note carried
// through — never an error, never a fabricated comparison.
func TestBuildEnvNoteOnMissingLogs(t *testing.T) {
	r := Build(Evidence{
		Failing: RunMeta{ID: 1, SHA: "a"}, Green: greenMeta("b", now), Now: now,
		EnvNote: "environment drift unavailable: log archive missing or expired",
	}, Default())
	if r.Environment != nil {
		t.Error("Environment should be absent without both setup logs")
	}
	if !strings.Contains(r.Note, "environment drift unavailable") {
		t.Errorf("Note = %q, want the env degrade note", r.Note)
	}
}

func TestBuildNewlyFailing(t *testing.T) {
	r := Build(Evidence{
		Failing: RunMeta{ID: 1, SHA: "a"}, Green: greenMeta("b", now), Now: now,
		FailingJobs: []JobOutcome{
			{Name: "test (1.26)", Conclusion: "failure"},
			{Name: "lint", Conclusion: "failure"},
			{Name: "build", Conclusion: "success"},
		},
		GreenJobs: []JobOutcome{
			{Name: "test (1.26)", Conclusion: "success"},
			{Name: "lint", Conclusion: "skipped"}, // not a pass -> not "newly" failing
		},
	}, Default())
	want := []string{"test (1.26)"}
	if !reflect.DeepEqual(r.Jobs.NewlyFailing, want) {
		t.Errorf("NewlyFailing = %v, want %v", r.Jobs.NewlyFailing, want)
	}
}

// The budget is a hard cap with exact accounting: with a tiny limit every list
// empties, omitted counts every dropped item, and used stays under the limit.
func TestBuildBudgetExactAccounting(t *testing.T) {
	ev := Evidence{
		Failing: RunMeta{ID: 1, SHA: "abc"}, Green: greenMeta("def", now), Now: now,
		Compare: &Comparison{Files: []FileChange{
			{Path: "a/b/one.go"}, {Path: "c/d/two.go"}, {Path: "go.sum", Additions: 1},
		}},
		FailingJobs: []JobOutcome{{Name: "j1", Conclusion: "failure"}},
		GreenJobs:   []JobOutcome{{Name: "j1", Conclusion: "success"}},
	}
	full := Build(ev, Config{BudgetBytes: 1 << 20})
	total := len(full.CommitRange.TopDirs) + len(full.CommitRange.Lockfiles) +
		len(full.CommitRange.WorkflowChanges) + len(full.Jobs.NewlyFailing)
	if total == 0 || full.Budget.OmittedItems != 0 {
		t.Fatalf("full build: total=%d omitted=%d, want items and 0 omitted", total, full.Budget.OmittedItems)
	}
	if full.Budget.UsedBytes <= 0 || full.Budget.UsedBytes > 1<<20 {
		t.Fatalf("full build UsedBytes = %d", full.Budget.UsedBytes)
	}

	tiny := Build(ev, Config{BudgetBytes: 1})
	kept := len(tiny.CommitRange.TopDirs) + len(tiny.CommitRange.Lockfiles) +
		len(tiny.CommitRange.WorkflowChanges) + len(tiny.Jobs.NewlyFailing)
	if kept != 0 {
		t.Errorf("tiny budget kept %d items, want 0", kept)
	}
	if tiny.Budget.OmittedItems != total {
		t.Errorf("tiny budget omitted = %d, want %d", tiny.Budget.OmittedItems, total)
	}
	if tiny.Budget.UsedBytes > 1 {
		t.Errorf("UsedBytes = %d > limit 1", tiny.Budget.UsedBytes)
	}
}

// The report's total marshalled size stays sane and lists render [] not null
// even when everything is trimmed.
func TestBuildTinyBudgetStillValidJSON(t *testing.T) {
	r := Build(Evidence{
		Failing: RunMeta{ID: 1, SHA: "a"}, Green: greenMeta("b", now), Now: now,
		Compare: &Comparison{Files: []FileChange{{Path: "x/y/z.go"}}},
	}, Config{BudgetBytes: 1})
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"top_dirs":[]`) {
		t.Errorf("trimmed lists must render []: %s", b)
	}
}
```

Create `internal/delta/fuzz_test.go`:

```go
package delta

import (
	"testing"
	"time"
)

// boundedInt maps an arbitrary fuzzed int into [0, max] without overflow.
func boundedInt(n, max int) int {
	if n < 0 {
		n = -(n + 1) // avoids math.MinInt negation overflow
	}
	if max <= 0 {
		return 0
	}
	return n % (max + 1)
}

// FuzzBuild guards the pure invariants: never panic, budget never exceeded,
// same_commit iff the shas match, and the always-present arrays stay non-nil.
func FuzzBuild(f *testing.F) {
	f.Add("abc", "abc", 4096, 3, 2, 2, true)
	f.Add("abc", "def", 1, 5, 3, 4, true)
	f.Add("", "", 64, 0, 0, 0, false)
	f.Fuzz(func(t *testing.T, fsha, gsha string, budgetBytes, nFiles, nActions, nJobs int, hasGreen bool) {
		nFiles, nActions, nJobs = boundedInt(nFiles, 40), boundedInt(nActions, 10), boundedInt(nJobs, 20)
		cfg := Config{BudgetBytes: boundedInt(budgetBytes, 1<<16) + 1}

		ev := Evidence{
			Failing: RunMeta{ID: 1, SHA: fsha},
			Now:     time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC),
		}
		if hasGreen {
			ev.Green = &RunMeta{ID: 2, SHA: gsha}
			files := make([]FileChange, nFiles)
			for i := range files {
				files[i] = FileChange{Path: "dir/sub/file.go", Additions: i}
			}
			ev.Compare = &Comparison{AheadBy: nFiles, Files: files}
			for i := 0; i < nActions; i++ {
				txt := "2026-07-01T00:00:00Z Download action repository 'o/r@v1' (SHA:aa)\n"
				ev.FailingSetup = append(ev.FailingSetup, SetupLog{Job: "j", Text: txt})
				ev.GreenSetup = append(ev.GreenSetup, SetupLog{Job: "j", Text: txt})
			}
			for i := 0; i < nJobs; i++ {
				ev.FailingJobs = append(ev.FailingJobs, JobOutcome{Name: "job", Conclusion: "failure"})
				ev.GreenJobs = append(ev.GreenJobs, JobOutcome{Name: "job", Conclusion: "success"})
			}
		}

		r := Build(ev, cfg)

		if r.Budget.UsedBytes > cfg.BudgetBytes {
			t.Fatalf("UsedBytes %d > limit %d", r.Budget.UsedBytes, cfg.BudgetBytes)
		}
		if r.Budget.OmittedItems < 0 {
			t.Fatalf("OmittedItems = %d", r.Budget.OmittedItems)
		}
		if r.Jobs.NewlyFailing == nil {
			t.Fatal("NewlyFailing is nil, must render []")
		}
		if hasGreen {
			if r.SameCommit != (fsha == gsha) {
				t.Fatalf("SameCommit = %v for fsha=%q gsha=%q", r.SameCommit, fsha, gsha)
			}
			if r.SameCommit && r.CommitRange != nil {
				t.Fatal("CommitRange present on same commit")
			}
		} else if r.LastGreen != nil {
			t.Fatal("LastGreen set without green evidence")
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/delta -run TestBuild -v`
Expected: compile FAIL — `undefined: Build`.

- [ ] **Step 3: Implement**

Create `internal/delta/build.go`:

```go
package delta

import (
	"encoding/json"
	"strings"

	"github.com/akira-toriyama/cifail/internal/model"
)

// Build turns gathered Evidence into the delta report. It is a total function:
// every degrade (no green baseline, expired logs, failed comparison) still
// yields a report — the agent branches on the JSON fields, and Note explains
// what is missing. All IO happened before this call.
func Build(ev Evidence, cfg Config) model.DeltaReport {
	r := model.DeltaReport{
		Failing: model.DeltaRun{Run: ev.Failing.ID, SHA: ev.Failing.SHA, Attempt: ev.Failing.Attempt, URL: ev.Failing.URL},
		Jobs:    model.DeltaJobs{NewlyFailing: []string{}},
		Budget:  model.DeltaBudget{LimitBytes: cfg.BudgetBytes},
	}
	var notes []string
	if ev.CompareNote != "" {
		notes = append(notes, ev.CompareNote)
	}
	if ev.EnvNote != "" {
		notes = append(notes, ev.EnvNote)
	}

	if ev.Green == nil {
		notes = append([]string{"no green run of this workflow found on the branch (within log retention); nothing to diff against"}, notes...)
		r.Note = strings.Join(notes, "; ")
		return r
	}

	g := model.DeltaRun{Run: ev.Green.ID, SHA: ev.Green.SHA, URL: ev.Green.URL}
	if !ev.Green.StartedAt.IsZero() && ev.Now.After(ev.Green.StartedAt) {
		g.AgeDays = int(ev.Now.Sub(ev.Green.StartedAt).Hours() / 24)
	}
	r.LastGreen = &g
	r.SameCommit = ev.Green.SHA == ev.Failing.SHA

	// Zero-commit pivot: same sha means git has no answer — omit commit_range
	// entirely and point the note at the environment. This is the case agents
	// misdiagnose most.
	if r.SameCommit {
		notes = append([]string{"failing and last-green runs share the same commit; the change is in the environment (actions, runner, external deps), not the code"}, notes...)
	} else if ev.Compare != nil {
		r.CommitRange = buildCommitRange(ev.Compare)
	}

	r.Environment = buildEnvironment(ev.FailingSetup, ev.GreenSetup)
	r.Jobs.NewlyFailing = newlyFailing(ev.FailingJobs, ev.GreenJobs)

	applyBudget(&r, cfg.BudgetBytes)
	r.Note = strings.Join(notes, "; ")
	return r
}

// buildEnvironment compares the two runs' resolved action SHAs and runner
// image. It needs BOTH sides — comparing against nothing would fabricate
// drift — so either side missing yields nil (the Evidence's EnvNote explains).
func buildEnvironment(failing, green []SetupLog) *model.DeltaEnvironment {
	if len(failing) == 0 || len(green) == 0 {
		return nil
	}
	fActions, fOrder, fRunner := mergeSetups(failing)
	gActions, gOrder, gRunner := mergeSetups(green)

	order := append([]string{}, gOrder...)
	for _, ref := range fOrder {
		if _, ok := gActions[ref]; !ok {
			order = append(order, ref)
		}
	}
	env := &model.DeltaEnvironment{Actions: make([]model.DeltaActionDrift, 0, len(order))}
	for _, ref := range order {
		gs, fs := gActions[ref], fActions[ref]
		env.Actions = append(env.Actions, model.DeltaActionDrift{
			Ref: ref, GreenSHA: gs, FailingSHA: fs,
			// Drift needs proof on both sides; a ref seen on one side only is
			// absence of evidence, not drift.
			Drifted: gs != "" && fs != "" && gs != fs,
		})
	}
	if fRunner != "" && gRunner != "" {
		env.Runner = &model.DeltaRunnerDrift{Green: gRunner, Failing: fRunner, Drifted: gRunner != fRunner}
	}
	return env
}

// mergeSetups folds per-job setups into one ref→sha view (first sighting wins;
// within one run the same ref resolves identically) plus the first runner id.
func mergeSetups(logs []SetupLog) (actions map[string]string, order []string, runner string) {
	actions = map[string]string{}
	for _, l := range logs {
		s := ParseSetup(l.Text)
		for _, a := range s.Actions {
			if _, ok := actions[a.Ref]; !ok {
				actions[a.Ref] = a.SHA
				order = append(order, a.Ref)
			}
		}
		if runner == "" {
			runner = s.Runner
		}
	}
	return actions, order, runner
}

// newlyFailing lists jobs failing now whose same-named job PASSED in the green
// run (skipped/cancelled greens don't count — no pass to regress from).
// Deduped, in failing-run order, always non-nil.
func newlyFailing(failing, green []JobOutcome) []string {
	passed := map[string]bool{}
	for _, j := range green {
		if j.Conclusion == "success" {
			passed[j.Name] = true
		}
	}
	out := make([]string, 0)
	seen := map[string]bool{}
	for _, j := range failing {
		if j.Conclusion == "failure" && passed[j.Name] && !seen[j.Name] {
			seen[j.Name] = true
			out = append(out, j.Name)
		}
	}
	return out
}

// budget charges serialized item sizes against a hard limit; a dropped item is
// counted, never silently lost.
type budget struct {
	limit, used, omitted int
}

func (b *budget) fits(item any) bool {
	n := 64 // unreachable fallback: all our shapes marshal
	if raw, err := json.Marshal(item); err == nil {
		n = len(raw) + 1
	}
	if b.used+n > b.limit {
		b.omitted++
		return false
	}
	b.used += n
	return true
}

// applyBudget enforces the byte budget over the report's variable-length lists
// only (the fixed envelope always fits), keeping the highest-signal content
// first: drifted actions → runner → workflow changes → lockfiles → newly
// failing jobs → top dirs → non-drifted actions. Truncation is structural —
// shorter lists plus OmittedItems — never inline marker strings.
func applyBudget(r *model.DeltaReport, limit int) {
	b := &budget{limit: limit}

	var drifted, stable []model.DeltaActionDrift
	if r.Environment != nil {
		for _, a := range r.Environment.Actions {
			if a.Drifted {
				drifted = append(drifted, a)
			} else {
				stable = append(stable, a)
			}
		}
	}

	keptDrifted := make([]model.DeltaActionDrift, 0, len(drifted))
	for _, a := range drifted {
		if b.fits(a) {
			keptDrifted = append(keptDrifted, a)
		}
	}
	if r.Environment != nil && r.Environment.Runner != nil && !b.fits(*r.Environment.Runner) {
		r.Environment.Runner = nil
	}
	if r.CommitRange != nil {
		wf := make([]model.DeltaWorkflowChange, 0, len(r.CommitRange.WorkflowChanges))
		for _, w := range r.CommitRange.WorkflowChanges {
			if b.fits(w) {
				wf = append(wf, w)
			}
		}
		r.CommitRange.WorkflowChanges = wf
		lf := make([]model.DeltaLockfile, 0, len(r.CommitRange.Lockfiles))
		for _, l := range r.CommitRange.Lockfiles {
			if b.fits(l) {
				lf = append(lf, l)
			}
		}
		r.CommitRange.Lockfiles = lf
	}
	nf := make([]string, 0, len(r.Jobs.NewlyFailing))
	for _, j := range r.Jobs.NewlyFailing {
		if b.fits(j) {
			nf = append(nf, j)
		}
	}
	r.Jobs.NewlyFailing = nf
	if r.CommitRange != nil {
		td := make([]string, 0, len(r.CommitRange.TopDirs))
		for _, d := range r.CommitRange.TopDirs {
			if b.fits(d) {
				td = append(td, d)
			}
		}
		r.CommitRange.TopDirs = td
	}
	keptStable := make([]model.DeltaActionDrift, 0, len(stable))
	for _, a := range stable {
		if b.fits(a) {
			keptStable = append(keptStable, a)
		}
	}
	if r.Environment != nil {
		r.Environment.Actions = append(keptDrifted, keptStable...)
	}

	r.Budget = model.DeltaBudget{LimitBytes: limit, UsedBytes: b.used, OmittedItems: b.omitted}
}
```

- [ ] **Step 4: Run tests and the fuzz smoke to verify they pass**

Run: `go test ./internal/delta -count=1` then `go test ./internal/delta -run FuzzBuild -fuzz FuzzBuild -fuzztime 5s`
Expected: PASS both (fuzz finds no invariant violation in 5s).

- [ ] **Step 5: Commit**

```bash
git add internal/delta/build.go internal/delta/build_test.go internal/delta/fuzz_test.go
git commit -m ":sparkles: feat(delta): assemble the delta report with a structural byte budget"
```

---

### Task 8: cli — wire the `delta` subcommand

**Files:**
- Create: `internal/cli/delta.go`
- Modify: `internal/cli/root.go` (~line 108: add one `root.AddCommand(newDeltaCmd())` line after `newFlakeCmd()`)
- Test: `internal/cli/delta_test.go`

**Interfaces:**
- Consumes: `delta.Build`/`delta.Default`/`delta.Evidence` (Tasks 5,7); `gh.LastGreenRun` (Task 2); `gh.CompareCommits` (Task 3); `model.Run.WorkflowID`, `gh.RunSummary.HeadSHA` (Task 1); existing `interruptOr`, `printPretty`, `printCompact`, `core.Usagef/APIf`, `gh.ResolveRepo/NewClient/ResolveRun/CurrentBranch/AllJobs/FetchLogs`.
- Produces: the user-facing subcommand. Nothing downstream.

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/delta_test.go`:

```go
package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/gh"
	"github.com/akira-toriyama/cifail/internal/model"
)

func withDeltaFlags(t *testing.T) {
	t.Helper()
	resetDeltaFlags()
	t.Cleanup(resetDeltaFlags)
}

func resetDeltaFlags() {
	deltaRepo, deltaPR, deltaBranch, deltaBudget, deltaNDJSON = "", 0, "", 4096, false
}

// Usage errors must be caught before ANY IO, with exit code 2.
func TestRunDeltaUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		prep func()
	}{
		{"run-id and --pr", []string{"123"}, func() { deltaPR = 7 }},
		{"run-id and --branch", []string{"123"}, func() { deltaBranch = "main" }},
		{"--pr and --branch", nil, func() { deltaPR = 7; deltaBranch = "main" }},
		{"bad run-id", []string{"nope"}, func() {}},
		{"negative run-id", []string{"-3"}, func() {}},
		{"zero budget", []string{"123"}, func() { deltaBudget = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withDeltaFlags(t)
			tc.prep()
			err := runDelta(nil, tc.args)
			var ce *core.Error
			if !errors.As(err, &ce) || ce.Code != core.CodeUsage {
				t.Fatalf("err = %v, want *core.Error with CodeUsage", err)
			}
		})
	}
}

// fakeDeltaArchive returns 'Set up job' text keyed by exact job name.
type fakeDeltaArchive map[string]string

func (a fakeDeltaArchive) StepLog(job string, n int) (string, bool) {
	if n != 1 {
		return "", false
	}
	s, ok := a[job]
	return s, ok
}

func (a fakeDeltaArchive) JobLog(job string) (string, bool) { return "", false }

type fakeDeltaProber struct {
	allJobs map[int64][]gh.JobResult
	green   gh.RunSummary
	greenOK bool
	cmp     gh.Comparison
	cmpErr  error
	logs    map[int64]fakeDeltaArchive
	logsErr map[int64]error
}

func (f *fakeDeltaProber) AllJobs(_ context.Context, runID int64) ([]gh.JobResult, error) {
	return f.allJobs[runID], nil
}

func (f *fakeDeltaProber) LastGreenRun(_ context.Context, _ int64, _ string) (gh.RunSummary, bool, error) {
	return f.green, f.greenOK, nil
}

func (f *fakeDeltaProber) CompareCommits(_ context.Context, _, _ string) (gh.Comparison, error) {
	return f.cmp, f.cmpErr
}

func (f *fakeDeltaProber) FetchLogs(_ context.Context, runID int64) (logArchive, error) {
	if err := f.logsErr[runID]; err != nil {
		return nil, err
	}
	return f.logs[runID], nil
}

var deltaTestRun = model.Run{
	ID: 123, WorkflowID: 7, HeadBranch: "main", HeadSHA: "abc",
	Conclusion: "failure", Attempt: 1, HTMLURL: "f",
}

// The full gather: green found on a different sha -> compare runs, both
// archives mined, jobs collected for both sides.
func TestBuildDeltaEvidence(t *testing.T) {
	setup := "2026-07-01T00:00:00Z Download action repository 'actions/checkout@v4' (SHA:aaa)"
	p := &fakeDeltaProber{
		allJobs: map[int64][]gh.JobResult{
			123: {{Name: "test", Conclusion: "failure"}},
			120: {{Name: "test", Conclusion: "success"}},
		},
		green:   gh.RunSummary{ID: 120, HeadSHA: "def", StartedAt: time.Now().Add(-24 * time.Hour)},
		greenOK: true,
		cmp:     gh.Comparison{AheadBy: 2, Files: []gh.ComparedFile{{Path: "go.sum", Additions: 1}}},
		logs: map[int64]fakeDeltaArchive{
			123: {"test": setup},
			120: {"test": setup},
		},
	}
	ev, err := buildDeltaEvidence(context.Background(), p, deltaTestRun, time.Now())
	if err != nil {
		t.Fatalf("buildDeltaEvidence: %v", err)
	}
	if ev.Green == nil || ev.Green.SHA != "def" {
		t.Fatalf("Green = %+v, want sha def", ev.Green)
	}
	if ev.Compare == nil || ev.Compare.AheadBy != 2 || len(ev.Compare.Files) != 1 {
		t.Fatalf("Compare = %+v, want ahead 2 with 1 file", ev.Compare)
	}
	if len(ev.FailingSetup) != 1 || len(ev.GreenSetup) != 1 {
		t.Fatalf("setups = %d/%d, want 1/1", len(ev.FailingSetup), len(ev.GreenSetup))
	}
	if len(ev.FailingJobs) != 1 || len(ev.GreenJobs) != 1 {
		t.Fatalf("jobs = %d/%d, want 1/1", len(ev.FailingJobs), len(ev.GreenJobs))
	}
}

// Same sha: the comparison must be SKIPPED (no wasted call, no fake range).
func TestBuildDeltaEvidenceSameCommitSkipsCompare(t *testing.T) {
	p := &fakeDeltaProber{
		allJobs: map[int64][]gh.JobResult{123: {}, 120: {}},
		green:   gh.RunSummary{ID: 120, HeadSHA: "abc"},
		greenOK: true,
		cmpErr:  errors.New("CompareCommits must not be called for the same sha"),
	}
	ev, err := buildDeltaEvidence(context.Background(), p, deltaTestRun, time.Now())
	if err != nil {
		t.Fatalf("buildDeltaEvidence: %v", err)
	}
	if ev.Compare != nil || ev.CompareNote != "" {
		t.Errorf("Compare = %+v note=%q, want none for same sha", ev.Compare, ev.CompareNote)
	}
}

// Expired/missing archives are a degrade with a note, never an error: old
// green runs legitimately outlive their logs.
func TestBuildDeltaEvidenceExpiredLogs(t *testing.T) {
	p := &fakeDeltaProber{
		allJobs: map[int64][]gh.JobResult{123: {}, 120: {}},
		green:   gh.RunSummary{ID: 120, HeadSHA: "def"},
		greenOK: true,
		logsErr: map[int64]error{120: core.APIf("not found: GET .../logs (404)")},
	}
	ev, err := buildDeltaEvidence(context.Background(), p, deltaTestRun, time.Now())
	if err != nil {
		t.Fatalf("buildDeltaEvidence: %v", err)
	}
	if len(ev.FailingSetup) != 0 || len(ev.GreenSetup) != 0 {
		t.Error("setups must be empty when either archive is unavailable")
	}
	if !strings.Contains(ev.EnvNote, "unavailable") {
		t.Errorf("EnvNote = %q, want an unavailability note", ev.EnvNote)
	}
}

// A compare failure (force-push made the green sha unreachable) degrades with
// a note instead of failing a report that is still mostly producible.
func TestBuildDeltaEvidenceCompareDegrades(t *testing.T) {
	p := &fakeDeltaProber{
		allJobs: map[int64][]gh.JobResult{123: {}, 120: {}},
		green:   gh.RunSummary{ID: 120, HeadSHA: "def"},
		greenOK: true,
		cmpErr:  core.APIf("not found: GET .../compare (404)"),
	}
	ev, err := buildDeltaEvidence(context.Background(), p, deltaTestRun, time.Now())
	if err != nil {
		t.Fatalf("buildDeltaEvidence: %v", err)
	}
	if ev.Compare != nil {
		t.Errorf("Compare = %+v, want nil on degrade", ev.Compare)
	}
	if !strings.Contains(ev.CompareNote, "commit comparison unavailable") {
		t.Errorf("CompareNote = %q, want degrade note", ev.CompareNote)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli -run 'TestRunDelta|TestBuildDeltaEvidence' -v`
Expected: compile FAIL — `undefined: runDelta` etc.

- [ ] **Step 3: Implement**

Create `internal/cli/delta.go`:

```go
package cli

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/delta"
	"github.com/akira-toriyama/cifail/internal/gh"
	"github.com/akira-toriyama/cifail/internal/model"
	"github.com/spf13/cobra"
)

// delta flags. Like wait/flake, this subcommand owns its own --ndjson: root's
// is a LOCAL (not persistent) flag, so a shared one would not reach the child.
var (
	deltaRepo   string
	deltaPR     int
	deltaBranch string
	deltaBudget int
	deltaNDJSON bool
)

func newDeltaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delta [run-id]",
		Short: "Report what changed between a failing run and the last green run",
		Long: "delta anchors on a RED workflow run, finds the last GREEN run of the same\n" +
			"workflow on the same branch, and reports the input delta between them in one\n" +
			"bounded JSON document: commit range (top dirs, lockfiles, workflow edits),\n" +
			"resolved action SHAs and runner image (drift no text diff can catch), and the\n" +
			"jobs that newly fail. When both runs share the same commit, commit_range is\n" +
			"omitted and the note pivots to environment drift.\n\n" +
			"Any produced report exits 0 — including degraded ones (no green run in\n" +
			"retention, expired logs); branch on the JSON fields. 1 means the target run\n" +
			"was not red / not found, 2 usage, 3 API/IO.",
		Example: "  # diff a specific red run against its last green baseline\n" +
			"  cifail delta 28083877791\n\n" +
			"  # enter from a pull request (its latest failing run)\n" +
			"  cifail delta --pr 42 --ndjson",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          runDelta,
	}
	f := cmd.Flags()
	f.StringVar(&deltaRepo, "repo", "", "target repository as owner/repo (default: the git origin of the working dir)")
	f.IntVar(&deltaPR, "pr", 0, "diff the latest failing run for this pull request number")
	f.StringVar(&deltaBranch, "branch", "", "diff the latest failing run on this branch (default: the current branch)")
	f.IntVar(&deltaBudget, "budget-bytes", delta.DefaultBudgetBytes, "hard byte budget for the report's variable-length lists")
	f.BoolVar(&deltaNDJSON, "ndjson", false, "emit compact single-line JSON instead of pretty JSON")
	return cmd
}

func runDelta(cmd *cobra.Command, args []string) error {
	// Entry grammar: at most one of a positional run-id, --pr, or --branch;
	// none at all falls back to the current branch (root's behaviour).
	selectors := 0
	if len(args) == 1 {
		selectors++
	}
	if deltaPR != 0 {
		selectors++
	}
	if deltaBranch != "" {
		selectors++
	}
	if selectors > 1 {
		return core.Usagef("pass only one of a run-id, --pr, or --branch")
	}
	if deltaBudget <= 0 {
		return core.Usagef("--budget-bytes must be positive, got %d", deltaBudget)
	}
	var target gh.Target
	switch {
	case len(args) == 1:
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil || id <= 0 {
			return core.Usagef("run-id must be a positive integer, got %q", args[0])
		}
		target = gh.Target{RunID: id}
	case deltaPR != 0:
		target = gh.Target{PR: deltaPR}
	case deltaBranch != "":
		target = gh.Target{Branch: deltaBranch}
	}

	ctx := cmd.Context()
	dir, err := os.Getwd()
	if err != nil {
		return core.APIf("getwd: %v", err)
	}
	if target == (gh.Target{}) {
		branch, err := gh.CurrentBranch(ctx, dir)
		if err != nil {
			return interruptOr(ctx, err)
		}
		target = gh.Target{Branch: branch}
	}
	owner, repo, err := gh.ResolveRepo(ctx, deltaRepo, dir)
	if err != nil {
		return interruptOr(ctx, err)
	}
	client, err := gh.NewClient(ctx, owner, repo)
	if err != nil {
		return interruptOr(ctx, err)
	}
	// ResolveRun returns the target's newest FAILING run; a non-failing /
	// absent run is a NoFailuref (soft miss, exit 1) — delta only diffs red runs.
	run, err := client.ResolveRun(ctx, target)
	if err != nil {
		return interruptOr(ctx, err)
	}

	cfg := delta.Default()
	cfg.BudgetBytes = deltaBudget

	ev, err := buildDeltaEvidence(ctx, deltaGH{client}, run, time.Now())
	if err != nil {
		return interruptOr(ctx, err)
	}
	r := delta.Build(ev, cfg)

	if deltaNDJSON {
		printCompact(r)
	} else {
		printPretty(r)
	}
	// A produced report is a successful output — always exit 0; the agent
	// branches on last_green / same_commit / note.
	return nil
}

// logArchive is the slice of gh.LogArchive the gatherer needs, as an interface
// so tests can fake step logs (LogArchive has no exported constructor).
type logArchive interface {
	StepLog(jobName string, stepNumber int) (string, bool)
	JobLog(jobName string) (string, bool)
}

// deltaProber is the slice of gh.Client that buildDeltaEvidence needs;
// injecting it keeps the fan-out unit-testable with a fake.
type deltaProber interface {
	AllJobs(ctx context.Context, runID int64) ([]gh.JobResult, error)
	LastGreenRun(ctx context.Context, workflowID int64, branch string) (gh.RunSummary, bool, error)
	CompareCommits(ctx context.Context, base, head string) (gh.Comparison, error)
	FetchLogs(ctx context.Context, runID int64) (logArchive, error)
}

// deltaGH adapts *gh.Client to deltaProber: only FetchLogs needs wrapping (its
// concrete *gh.LogArchive return must become the logArchive interface).
type deltaGH struct {
	*gh.Client
}

func (d deltaGH) FetchLogs(ctx context.Context, runID int64) (logArchive, error) {
	a, err := d.Client.FetchLogs(ctx, runID)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// buildDeltaEvidence gathers everything delta.Build reasons over: the failing
// run's jobs, the last green baseline (plus its jobs), the commit comparison,
// and both runs' 'Set up job' logs. All IO lives here; the decision stays pure.
// Compare and log fetches are best-effort (force-pushed shas and expired log
// retention are legitimate), with the degrade reason recorded for the report.
func buildDeltaEvidence(ctx context.Context, p deltaProber, run model.Run, now time.Time) (delta.Evidence, error) {
	ev := delta.Evidence{
		Failing: delta.RunMeta{ID: run.ID, SHA: run.HeadSHA, Attempt: run.Attempt, URL: run.HTMLURL},
		Now:     now,
	}

	failingJobs, err := p.AllJobs(ctx, run.ID)
	if err != nil {
		return delta.Evidence{}, err
	}
	ev.FailingJobs = toJobOutcomes(failingJobs)

	// Without a workflow id or branch there is nowhere to hunt for a baseline;
	// Build reports the no-green degrade.
	if run.WorkflowID == 0 || run.HeadBranch == "" {
		return ev, ctx.Err()
	}
	green, ok, err := p.LastGreenRun(ctx, run.WorkflowID, run.HeadBranch)
	if err != nil {
		return delta.Evidence{}, err
	}
	if !ok {
		return ev, ctx.Err()
	}
	ev.Green = &delta.RunMeta{ID: green.ID, SHA: green.HeadSHA, URL: green.HTMLURL, StartedAt: green.StartedAt}

	greenJobs, err := p.AllJobs(ctx, green.ID)
	if err != nil {
		return delta.Evidence{}, err
	}
	ev.GreenJobs = toJobOutcomes(greenJobs)

	if green.HeadSHA != run.HeadSHA {
		cmp, err := p.CompareCommits(ctx, green.HeadSHA, run.HeadSHA)
		if err != nil {
			// A force-push can make the green sha unreachable (404); the rest
			// of the report is still worth producing.
			ev.CompareNote = "commit comparison unavailable: " + err.Error()
		} else {
			c := delta.Comparison{AheadBy: cmp.AheadBy, BehindBy: cmp.BehindBy, Capped: cmp.Capped}
			for _, f := range cmp.Files {
				c.Files = append(c.Files, delta.FileChange{
					Path: f.Path, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch,
				})
			}
			ev.Compare = &c
		}
	}

	fArchive, fErr := p.FetchLogs(ctx, run.ID)
	gArchive, gErr := p.FetchLogs(ctx, green.ID)
	if fErr != nil || gErr != nil {
		// Log retention outlives runs; an expired archive is a degrade, not an
		// error — but never compare against a half-fetched environment.
		ev.EnvNote = "environment drift unavailable: a run's log archive is missing or expired"
	} else {
		ev.FailingSetup = setupLogs(fArchive, failingJobs)
		ev.GreenSetup = setupLogs(gArchive, greenJobs)
	}

	// Best-effort fetches swallow cancellation; surface a Ctrl-C that landed
	// mid-gather as a silent 130 rather than a bogus report at exit 0.
	if ctx.Err() != nil {
		return delta.Evidence{}, ctx.Err()
	}
	return ev, nil
}

// setupLogs pulls each job's 'Set up job' text: the per-step file (step 1 by
// convention) when the archive has one, else the whole-job log whose head
// contains the same block.
func setupLogs(a logArchive, jobs []gh.JobResult) []delta.SetupLog {
	out := make([]delta.SetupLog, 0, len(jobs))
	for _, j := range jobs {
		if txt, ok := a.StepLog(j.Name, 1); ok {
			out = append(out, delta.SetupLog{Job: j.Name, Text: txt})
			continue
		}
		if txt, ok := a.JobLog(j.Name); ok {
			out = append(out, delta.SetupLog{Job: j.Name, Text: txt})
		}
	}
	return out
}

func toJobOutcomes(jobs []gh.JobResult) []delta.JobOutcome {
	out := make([]delta.JobOutcome, len(jobs))
	for i, j := range jobs {
		out[i] = delta.JobOutcome{Name: j.Name, Conclusion: j.Conclusion}
	}
	return out
}
```

In `internal/cli/root.go`, after the `root.AddCommand(newFlakeCmd())` line, add:

```go
	root.AddCommand(newDeltaCmd())
```

- [ ] **Step 4: Run the full package tests**

Run: `go test ./... -count=1`
Expected: PASS everywhere (root/wait/flake regression suites included).

- [ ] **Step 5: Smoke the built binary**

```bash
go build -o /tmp/cifail-smoke ./cmd/cifail
/tmp/cifail-smoke delta --help
/tmp/cifail-smoke delta 123 --pr 4; echo "exit=$?"
```
Expected: help shows the Long text with the exit-code paragraph and all 5 flags; the second command prints a usage error envelope on stderr and `exit=2`.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/delta.go internal/cli/delta_test.go internal/cli/root.go
git commit -m ":sparkles: feat(cli): wire the delta subcommand"
```

---

### Task 9: docs, spec sync, full check

**Files:**
- Modify: `README.md` (add a `## cifail delta` section after the flake section, matching its structure)
- Modify: `CLAUDE.md` (architecture diagram + exit-code section)
- Modify: `docs/superpowers/specs/2026-07-05-cifail-delta-design.md` (two amendments)

**Interfaces:** none — documentation of everything above.

- [ ] **Step 1: README section**

Add after the `cifail flake` section, following the same structure (motivating paragraph → console example → flags paragraph → exit codes paragraph):

````markdown
## `cifail delta` — what changed since the last green run

When a failure is real (see `cifail flake`), the first debugging question is
"what changed since this workflow was last green?". `delta` answers it in one
bounded JSON document: it finds the last green run of the same workflow on the
same branch and reports the commit range (top dirs, lockfiles, workflow
edits), the environment drift no text diff can catch (resolved action SHAs,
runner image — mined from both runs' 'Set up job' logs), and the jobs that
newly fail. When both runs share the same commit it says so loudly
(`same_commit: true`), omits `commit_range`, and points the note at the
environment.

```console
$ cifail delta 28083877791 --ndjson
{"failing":{"run":28083877791,"sha":"abc1234","attempt":1,"url":"…"},"last_green":{"run":28083112345,"sha":"def5678","age_days":2,"url":"…"},"same_commit":false,"commit_range":{"ahead_by":3,"behind_by":0,"files_changed":12,"top_dirs":["src/api (8)",". (2)",".github/workflows (2)"],"lockfiles":[{"path":"go.sum","additions":14,"deletions":9}],"workflow_changes":[{"path":".github/workflows/ci.yml","uses_changed":["actions/setup-node v4→v5"]}]},"environment":{"actions":[{"ref":"actions/checkout@v4","green_sha":"11bd719…","failing_sha":"08c6903…","drifted":true}],"runner":{"green":"ubuntu-24.04/20250601.1.0","failing":"ubuntu-24.04/20250620.2.0","drifted":true}},"jobs":{"newly_failing":["test (ubuntu-latest, 1.26)"]},"budget":{"limit_bytes":4096,"used_bytes":2210,"omitted_items":0}}
```

Flags: a positional run-id, `--pr N`, or `--branch B` pick the failing run
(default: the newest failing run on the current branch); `--repo owner/repo`
overrides the origin-derived repository; `--budget-bytes` bounds the
variable-length lists (default 4096, trimmed highest-signal-first with the
drop count in `budget.omitted_items`); `--ndjson` emits compact JSON.

Exit codes: any produced report exits `0` — including degraded ones
(`last_green: null` when the branch has no green history in retention, no
`environment` when a log archive expired; `note` explains). Branch on the
JSON fields, not the exit code. `1` means the target run was not red / not
found, `2` usage, `3` API/IO, `130` interrupted.
````

- [ ] **Step 2: CLAUDE.md updates**

In the architecture block, after the `internal/flake` entry, add:

```
internal/delta        → PURE input-delta between a failing run and the last
                        green run of the same workflow+branch; cli gathers an
                        Evidence snapshot (all IO), Build reasons over it.
                        Zero-commit pivot + Set-up-job parsing live here.
```

In the exit-code contract section, after the `flake` paragraph, add:

```
The `delta` subcommand keeps the base contract: any produced report exits `0`
(degraded reports included — no green baseline in retention, expired log
archives, zero-commit; the JSON `note` explains), so agents branch on
`last_green` / `same_commit` / `note`, not the exit code. `1` remains the
ResolveRun soft miss. There is deliberately no per-verdict exit code and no
ExitCode func in internal/delta — delta has no verdict enum to guard.
```

- [ ] **Step 3: Spec amendments (implementation refinements)**

In `docs/superpowers/specs/2026-07-05-cifail-delta-design.md`:
1. In the Architecture block, replace `Build(ev Evidence) → model.DeltaReport, ExitCode` with `Build(ev Evidence) → model.DeltaReport (a produced report always exits 0; no ExitCode mapping needed)`.
2. In the `environment` bullet, replace `environment is null and the note says why` with `environment is omitted and the note says why`.

- [ ] **Step 4: Full check**

Run: `sh scripts/check.sh`
Expected: all green — build, vet, `go test -race ./...`, fuzz smoke, golangci-lint, govulncheck, binary smoke. Fix anything it flags before committing.

- [ ] **Step 5: Commit**

```bash
git add README.md CLAUDE.md docs/superpowers/specs/2026-07-05-cifail-delta-design.md
git commit -m ":memo: docs: document the delta subcommand in README and CLAUDE.md"
```

- [ ] **Step 6: Wrap up the branch**

Update the furrow task body checklist (`projects/.furrow/bodies/t-hv1y.md`) and `furrow sync`; then use superpowers:finishing-a-development-branch. The PR body must end with:

```
SetStatus-task: https://github.com/akira-toriyama/projects/blob/main/.furrow/bodies/t-hv1y.md review
```
