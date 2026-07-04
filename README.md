# cifail

**Bounded, high-signal extraction of a GitHub Actions run's failing logs — built for agents, not dashboards.**

`gh run view --log-failed` dumps the *entire* failing job log (measured at ~130 KB
for a single Swift-test job). The Checks API annotations are no substitute: they
carry the real error only when a problem matcher fires — and the common matchers
miss `go test` failures (`file.go:15: msg`), have nothing for Swift, and often
record just `Process completed with exit code 1.`

cifail closes that gap. It resolves a PR / branch / run to its latest **failing**
workflow run, downloads the run log archive **once**, keeps only the **failed
steps**, and pares them to the error lines (plus context) that fit a byte
budget — emitting JSON an agent can read in one turn.

```console
$ cifail --repo akira-toriyama/chord --run 28083877791 | wc -c
9173                       # was ~134 KB via `gh run view --log-failed`
```

The kept excerpts still contain exactly what matters:

```
✘ Test showWithoutFileReportsExit3() ... Expectation failed: (out?.exitCode → 0) == 3
✘ Suite CLIDispatchTests failed after 2.394 seconds with 2 issues.
##[error]Process completed with exit code 1.
```

## How it works

1. **Resolve** the target to a run: `--run <id>`, `--pr <n>`, `--branch <b>`, or
   (default) the current branch of the working directory. For a PR/branch it
   picks the newest run whose conclusion is `failure`.
2. **Find the failed steps** via `/actions/runs/{id}/jobs` (failed jobs → failed
   steps only).
3. **Merge annotations** (`/check-runs/{jobId}/annotations`) — free and tiny, but
   supplementary since they're sparse.
4. **Download the log archive once** and pull out each failed step's log (the
   per-step file when present, else the whole job log).
5. **Extract within budget**: a severity ladder (`##[error]`, `--- FAIL`,
   `panic:`, `✘`, `error:`, …) finds the error lines; each is kept with context,
   the head and tail are anchored, and the budget is spent error-first and
   biased toward the end of the log — where the failure usually surfaces.

Authentication reuses your `gh` CLI token (`gh auth token`); cifail needs no
setup of its own.

## Install

```sh
brew install akira-toriyama/tap/cifail
# or
go install github.com/akira-toriyama/cifail/cmd/cifail@latest
# or from a checkout
./install.sh            # → ~/.local/bin/cifail
# or with nix
nix run github:akira-toriyama/cifail -- --help
```

See the [Releases](https://github.com/akira-toriyama/cifail/releases) page for
published versions.

## Usage

```sh
cifail                                        # latest failing run for the current branch
cifail --pr 42                                # a specific pull request
cifail --branch main                          # a specific branch
cifail --repo owner/repo --run 123456789      # a specific run in another repo
cifail --budget-bytes 4096 --context 2        # tighter budget, less context
cifail --ndjson                               # compact single-line JSON
```

### Flags

| Flag             | Default            | Meaning                                             |
| ---------------- | ------------------ | --------------------------------------------------- |
| `--repo`         | git origin of cwd  | target repository as `owner/repo`                   |
| `--pr`           | —                  | latest failing run for this PR number               |
| `--branch`       | current branch     | latest failing run for this branch                  |
| `--run`          | —                  | a specific workflow run id                          |
| `--budget-bytes` | 8192               | byte budget for kept log excerpts                   |
| `--context`      | 3                  | context lines around each matched error line        |
| `--ndjson`       | off                | compact one-line JSON instead of pretty JSON        |

### Output

Pretty JSON (default) or one-line JSON (`--ndjson`) on stdout:

```jsonc
{
  "run":    { "id": 123, "conclusion": "failure", "head_sha": "…", "html_url": "…" },
  "jobs":   [ { "name": "build",
                "failed_steps": [ { "number": 5, "name": "…",
                                    "annotations": [ … ],
                                    "excerpts":    [ { "start_line": 1180, "reason": "match", "lines": [ … ] } ],
                                    "omitted_lines": 1210 } ] } ],
  "budget": { "limit_bytes": 8192, "used_bytes": 5094, "omitted_lines": 1210 }
}
```

An excerpt's `reason` is `full`, `head`, `tail`, or `match`; a gap between one
excerpt's line range and the next means lines were omitted there.

A failed run with **no failing job** (usually a workflow-file syntax error)
returns just the run metadata and a `note` pointing at `html_url`.

### Exit codes

| Code  | Meaning                                             |
| ----- | --------------------------------------------------- |
| `0`   | a failing run was found and its logs were extracted |
| `1`   | no failing run found for the target (a soft miss)   |
| `2`   | bad usage / invalid input — fix the args            |
| `3`   | GitHub API / network / IO failure                   |
| `130` | interrupted (Ctrl-C) — see below                    |

Errors are printed to **stderr** as `{"error":{"code","message"}}`, so a caller
piping stdout to `jq` is unaffected.

**Ctrl-C** cancels the in-flight work (the `git`/`gh` child and any HTTP request)
and exits `130`, the conventional `128 + SIGINT`; a second Ctrl-C hard-kills. This
matters most for `cifail wait`, which blocks for minutes.

## `cifail wait` — block until CI concludes

`git push && cifail wait` resolves the workflow runs of the pushed commit (HEAD
by default; `--sha` to override), blocks until they finish, and prints a
worst-of verdict. On red it embeds each failing step's budgeted excerpts — the
same shape as the extract command, under `runs[].jobs`.

```console
$ git push && cifail wait --ndjson
{"sha":"1c27b08","status":"completed","conclusion":"failure","elapsed_s":312,
 "runs":[{"id":42,"name":"ci","status":"completed","conclusion":"failure",
          "jobs":[{"name":"build","failed_steps":[{"name":"go test",
                   "excerpts":[{"start_line":40,"reason":"match","lines":["--- FAIL ..."]}]}]}]}],
 "budget":{"limit_bytes":16384,"used_bytes":5100,"omitted_lines":40}}
```

Flags: `--repo`, `--sha` (default: HEAD of cwd), `--timeout` (default `30m`),
`--interval` (default `10s`), `--budget-bytes`, `--context`, `--ndjson`.

Exit codes: `0` green (or no runs triggered) · `1` red (CI failed/cancelled — a
shell `&&` chain stops here) · `124` not concluded (`conclusion` is `pending`,
re-run the same command to resume, or `timed_out`) · `2` usage · `3` API/IO ·
`130` interrupted (Ctrl-C). The verdict goes to stdout as pure JSON on `0`/`1`/
`124` (even though `1`/`124` are nonzero); `2`/`3` use the stderr error envelope,
and `130` prints nothing.

A single call blocks at most ~9 minutes (under an agent shell's process limit);
for longer runs it returns `pending` and you re-run `cifail wait` to continue —
`elapsed_s` is measured from the run's start, so it stays accurate across
re-runs.

## `cifail flake` — rerun or debug?

Faced with a red run, an agent has to decide: is this a flaky failure worth a
rerun, or a real bug worth debugging? Guessing wrong burns a whole session
debugging a flake — or reruns a genuine bug into hiding. `cifail flake <run-id>`
(or `--pr <n>`) returns a 1-shot, **evidence-tiered** verdict for that decision,
from the run/attempt/sibling history — no upload instrumentation, works on a
freshly cloned repo.

```console
$ cifail flake 28083877791 --ndjson
{"verdict":"likely_flaky","confidence":"high",
 "run":{"id":28083877791,"head_sha":"…","run_attempt":2,"workflow":"CI","html_url":"…"},
 "failing_jobs":["test (ubuntu-latest, 20)"],
 "evidence":[{"tier":"same_run_pass_attempt","job_name":"test (ubuntu-latest, 20)",
              "detail":"attempt 1 passed job 'test (ubuntu-latest, 20)'","attempt":1,"html_url":"…"}],
 "base_rate":{"branch":"main","runs":20,"failures":3,"rate":0.15,"window":"last 20 runs on main"},
 "runs_examined":24,"window":{"attempts_examined":1,"siblings_examined":3,"branch_runs":20}}
```

The verdict is honest by construction. It is `likely_flaky` **only** on same-sha
divergence — the same failing job passed on a prior **attempt** of this run, or
on a **sibling** run of the same head sha (matched on workflow **and** event, so
a `push`-vs-`pull_request` behavioral difference isn't mistaken for flakiness).
That is near-proof: same commit, same job, different outcome. Absent it, the
verdict is `insufficient_evidence` — the branch **base rate** is reported for
calibration but never escalates the verdict, and `runs_examined` / `window` tell
the agent how much history actually backed the call.

Flags: `--repo`, `--pr <n>` (instead of a run-id), `--branch-window` (default
`20`, recent branch runs sampled for the base rate), `--max-siblings` (default
`10`), `--ndjson`.

Exit codes: **both** verdicts exit `0` — a produced judgement is a successful
output, and the agent branches on the JSON `verdict` field. `1` means the target
run was not red / not found (soft miss), `2` usage, `3` API/IO, `130`
interrupted. (v1 attributes at the step/job level over REST only; it does not
parse test-level logs.)

## Development

```sh
sh scripts/check.sh     # build, vet, go test -race, golangci-lint, govulncheck, smoke
```

## License

[MIT](LICENSE)
