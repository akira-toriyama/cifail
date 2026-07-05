# `cifail delta` — input delta between a failing run and the last green run

Task: [t-hv1y](https://github.com/akira-toriyama/projects/blob/main/.furrow/bodies/t-hv1y.md)
Status: design approved 2026-07-05 (v1 scope fixed; see De-scoped below).

## Problem

When CI goes red and `cifail flake` says the failure is real, the next debugging
question is always: **"what changed since the last time this workflow was
green?"** Answering it today costs 4–6 agent turns (`gh run list` → `git log
A..B` → `git diff --stat` → eyeball workflows/ and lockfiles), most of whose
output is discarded. Worse, two high-value classes of change are invisible to
any git-side diff: a floating action tag resolving to a new SHA, and a runner
image update.

`cifail delta` answers the question in one bounded JSON report: resolve the
failing run, find the last green run of the *same workflow on the same branch*,
and report what changed between them — commits, lockfiles, workflow edits,
resolved action SHAs, runner image.

## CLI contract

```console
$ cifail delta [run-id] [--pr N | --branch B] [--repo owner/repo] \
               [--budget-bytes N] [--ndjson]
```

- Entry grammar identical to `flake`: positional run-id XOR `--pr` XOR
  `--branch`; default target is the current branch of the cwd repo.
- Exit codes follow the frozen contract: **0** = a produced report (including
  degraded reports: no green run in retention, expired logs, zero-commit), **1**
  = `ResolveRun` soft miss (target not red / not found), **2** usage, **3**
  API/IO, **130** interrupted (silent). No new codes; agents branch on JSON
  fields, per the `flake` precedent.
- stdout is pure JSON (pretty; `--ndjson` compact via its own local flag);
  errors are the `{"error":{code,message}}` envelope on stderr.

## Baseline discovery

- Parse `workflow_id` in `apiRun` (currently dropped) and carry it on the run
  type — matching "same workflow" by display name is rename-fragile.
- New gh method `LastGreenRun(ctx, workflowID int64, branch string)` =
  `GET /repos/{o}/{r}/actions/workflows/{workflow_id}/runs?branch=X&status=success&per_page=1`
  — one call, server-side filtering, newest first.
- Widen `RunSummary` with `HeadSHA` (already decoded by `apiRun`, currently
  dropped in the mapping).
- No green run found (empty result / out of retention): still exit 0, with
  `"last_green": null` and an explanatory `note`.

## Report shape (v1)

```json
{"failing":{"run":123,"sha":"abc","attempt":2,"url":"…"},
 "last_green":{"run":120,"sha":"def","age_days":2,"url":"…"},
 "same_commit":false,
 "commit_range":{"ahead_by":3,"behind_by":0,"files_changed":12,
                 "top_dirs":["src/api (8)"],
                 "lockfiles":[{"path":"go.sum","additions":14,"deletions":9}],
                 "workflow_changes":[{"path":".github/workflows/ci.yml",
                                      "uses_changed":["setup-node v4→v5"]}]},
 "environment":{"actions":[{"ref":"actions/checkout@v4",
                            "green_sha":"…","failing_sha":"…","drifted":true}],
                "runner":{"green":"ubuntu-24.04/20250601.1",
                          "failing":"ubuntu-24.04/20250620.2"}},
 "jobs":{"newly_failing":["test (ubuntu-latest, 1.26)"]},
 "budget":{"limit_bytes":4096,"used_bytes":2210,"omitted_items":0}}
```

(`note` is `omitempty`: present only on a degraded report — no green baseline,
expired logs, or the same-commit pivot.)

Semantics:

- **`same_commit` (flagship)**: when failing SHA == green SHA, omit
  `commit_range` entirely and point the `note` at environment drift — the case
  agents misdiagnose most, where git has no answer.
- **`commit_range`**: new gh method `CompareCommits(ctx, base, head)` =
  `GET /repos/{o}/{r}/compare/{green}...{failing}`. `behind_by > 0` is the
  rebase/merge hidden-delta signal (covers the PR merge-base-drift concern
  without extra calls). The compare API caps the files list (300); when hit,
  report it via the budget/`note` machinery rather than pretending
  completeness.
  - `top_dirs`: aggregate changed files by directory path truncated to depth 2
    (`src/api`, `internal/gh`; root-level files under `.`), largest first,
    capped.
  - `lockfiles`: **detection only** in v1 — known lockfile names
    (go.sum, go.mod, package-lock.json, pnpm-lock.yaml, yarn.lock, uv.lock,
    Cargo.lock, Gemfile.lock, composer.lock) with additions/deletions.
  - `workflow_changes`: files under `.github/workflows/` with a cheap,
    high-signal extra: `uses_changed` derived from changed `uses:` lines in the
    file's patch.
- **`environment`**: `FetchLogs` on both runs; for each job, take the
  `Set up job` step (`StepLog(name, 1)`, falling back to `JobLog` head). A new
  pure parser extracts `Download action repository '<owner/repo@ref>'
  (SHA:<sha>)` lines and the `Runner Image:` / `Runner Image Provisioner` /
  version lines (after stripping the RFC3339Nano timestamp prefix each archive
  line carries). Compare resolved SHAs per action ref across runs — the
  differentiator no text diff can catch. Log fetch for either side is
  **best-effort** (old runs' archives expire): on failure, `environment` is
  omitted and the `note` says why (house pattern: Annotations / AttemptJobs).
- **`jobs.newly_failing`**: jobs failing in the target run whose same-name job
  succeeded in the green run (`AllJobs` both sides; matrix display name is the
  identity key, as in `flake`).
- **Budget**: hard `--budget-bytes` cap (default 4096, from `delta.Default()`)
  enforced structurally, the house way — it governs the variable-length list
  content (top_dirs, lockfiles, workflow_changes, actions, newly_failing;
  bytes counted as each element's serialized length), not the fixed envelope.
  Lists are trimmed largest-signal-first until content fits; omissions are
  accounted in the `budget` block (`omitted` count), no inline
  truncation-marker strings. Invariant (fuzzable): `used_bytes ≤ limit_bytes`;
  kept + omitted == total per list.

## Architecture (mirrors `flake`, the 13-file blueprint of 2703da7)

```
internal/cli/delta.go     cobra adapter: entry grammar, deltaProber (narrow
                          unexported interface over *gh.Client), buildDeltaEvidence
                          (ALL IO, trailing ctx.Err() check), print, always exit 0
internal/delta/           PURE (stdlib + model + core only): Set-up-job parser,
                          file classification (top_dirs / lockfiles / workflows),
                          uses:-line extraction, budget capping,
                          Build(ev Evidence) → model.DeltaReport (a produced
                          report always exits 0; no ExitCode mapping needed)
internal/gh/              apiRun: +workflow_id; RunSummary: +HeadSHA;
                          +LastGreenRun; +CompareCommits (api* struct → carrier)
internal/model/delta.go   dep-free JSON shapes: snake_case tags, always-present
                          arrays non-nil ([] not null), optional blocks *T+omitempty
```

Conventions binding this work (from the codebase read):

- ctx-first everywhere; every IO error wrapped in `interruptOr(ctx, err)`.
- Errors classified at source: `core.Usagef` / `core.APIf` / `core.NoFailuref`;
  new endpoints route non-200s through `statusError`.
- Flag defaults seeded from `delta.Default()` so `--help` shows real values.
- No verdict/`ExitCode`: `runDelta` prints the report and returns nil — any
  produced report is exit 0 (unlike `flake`/`wait`, delta has no Silent-exit
  path). Only usage (2), the `ResolveRun` soft miss (1), API/IO (3), and
  interrupt (130) leave via `core.Error`.

## Testing (stdlib only, no testify, no golden files)

- `internal/delta`: named-case table tests for the Set-up-job parser (inline
  fixtures with real timestamp prefixes), file classification, uses:-diff;
  focused tests for zero-commit pivot and degraded (no-green, no-logs) notes;
  fuzz test for the budget invariant (output ≤ limit, exact accounting, arrays
  non-nil) and parser robustness (never panics).
- `internal/gh`: httptest servers for `LastGreenRun` and `CompareCommits`
  (`&Client{Owner, Repo, token, http: srv.Client(), base: srv.URL}`), asserting
  path/query and mapping.
- `internal/cli`: usage-rejection table calling `runDelta(nil, args)` directly
  with `withDeltaFlags`/`resetDeltaFlags`; map-backed `fakeDeltaProber` for
  `buildDeltaEvidence`, including the expired-logs degrade path.
- `internal/model`: marshal-shape tests (presence AND omission).

## Definition of done

One `feat` commit (minor bump) touching, like flake did: the four code layers +
tests, `root.AddCommand(newDeltaCmd())`, README `## cifail delta` section
(motivation, console example, flags, exit codes), CLAUDE.md architecture
diagram + exit-code section. `sh scripts/check.sh` green before PR. PR body
carries the SetStatus-task footer for t-hv1y.

## De-scoped to v2 (deliberate)

- **Lockfile version parsing** ("vitest 3.1→3.2"): needs per-format parsers;
  v1's "which lockfile moved, by how many lines" is enough to direct the agent.
- **Matrix job-level green matching** (last run where *this job* passed): more
  API calls and a second discovery loop; v1's run-level baseline + `newly_failing`
  covers the common case.
- **Default-branch fallback** when the branch has no green history (compare
  against main's last green): new repo-metadata call; keep v1 same-branch only.
