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

| Code | Meaning                                             |
| ---- | --------------------------------------------------- |
| `0`  | a failing run was found and its logs were extracted |
| `1`  | no failing run found for the target (a soft miss)   |
| `2`  | bad usage / invalid input — fix the args            |
| `3`  | GitHub API / network / IO failure                   |

Errors are printed to **stderr** as `{"error":{"code","message"}}`, so a caller
piping stdout to `jq` is unaffected.

## Development

```sh
sh scripts/check.sh     # build, vet, go test -race, golangci-lint, govulncheck, smoke
```

## License

[MIT](LICENSE)
