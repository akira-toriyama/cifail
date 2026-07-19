# CLAUDE.md — cifail

cifail is a small Go CLI that extracts a GitHub Actions run's **failing** logs in
a bounded, high-signal form for agents (see [README.md](README.md) for the what
and why). This file is the working contract for editing it.

## Architecture (dependency direction points inward)

```
cmd/cifail            → os.Exit(cli.Execute())
internal/cli          → cobra adapter: flags, exit-code contract, JSON output
internal/collect      → orchestrates gh → extract → model.Result
internal/gh           → GitHub REST (reuses `gh auth token`); resolve run, jobs,
                        annotations, log archive, runs-for-sha, all-jobs/attempt-
                        jobs/branch-runs (for `flake`). IO lives here and ONLY here.
internal/wait         → PURE poll/aggregate/exit logic for the `wait` subcommand;
                        a gh Poller + a Clock are injected (no IO here)
internal/flake        → PURE flaky-vs-real verdict for the `flake` subcommand;
                        cli gathers an Evidence snapshot (all IO), Decide reasons
                        over it (no IO). The honesty gate lives here.
internal/delta        → PURE input-delta between a failing run and the last
                        green run of the same workflow+branch; cli gathers an
                        Evidence snapshot (all IO), Build reasons over it.
                        Zero-commit pivot + Set-up-job parsing live here.
internal/extract      → PURE budget algorithm + severity ladder (no IO)
internal/model        → JSON output shapes (dependency-free)
internal/core         → exit-code contract + structured Error
internal/version      → build version/commit/date (ldflags-injected)
```

- `internal/extract` and `internal/model` are pure and dependency-free — keep
  them that way so the budget logic stays unit-testable without a network.
- All GitHub IO goes through `internal/gh`. Do not call the API from `cli`.

## Exit-code contract (stable — scripts and agents branch on it)

`0` extracted · `1` no failing run (soft miss) · `2` usage/bad input · `3` API/IO.
Non-zero exits print `{"error":{"code","message"}}` to **stderr**; stdout stays
pure JSON.

The `wait` subcommand extends this with `124` (runs not concluded within the
call/deadline — the JSON `conclusion` says `pending`=re-run vs `timed_out`); in
`wait`, `1` means the CI run went red (failure/cancelled). `wait` writes its
verdict to **stdout** even on a nonzero exit (no stderr envelope) via
`core.Error.Silent`.

The `flake` subcommand keeps the base contract and maps **both** verdicts
(`likely_flaky` and `insufficient_evidence`) to `0` — a produced judgement is a
successful output; the agent branches on the JSON `verdict` field (as `extract`
returns `0` for any produced Result). `1` there means the target run was not red
/ not found (the `ResolveRun` soft miss). Deliberately no distinct code per
verdict: `1` already means no-failure (root) and red (`wait`), so overloading it
would break `&&` chains. The rerun-vs-debug honesty gate — `likely_flaky` only on
same-sha divergence, base rate never escalates it — lives in `internal/flake`.

The `delta` subcommand keeps the base contract: any produced report exits `0`
(degraded reports included — no green baseline in retention, expired log
archives, zero-commit; the JSON `note` explains), so agents branch on
`last_green` / `same_commit` / `note`, not the exit code. `1` remains the
ResolveRun soft miss. There is deliberately no per-verdict exit code and no
ExitCode func in internal/delta — delta has no verdict enum to guard.

`130` is interrupted: `Execute` derives a root ctx from `signal.NotifyContext`
(SIGINT/SIGTERM), threaded through `gh` (git/gh via `exec.CommandContext`, HTTP
via `NewRequestWithContext`) and `wait`'s poll loop + injected `Clock.Sleep`, so a
Ctrl-C cancels in-flight work and exits `130` (silent); a second Ctrl-C hard-kills.
The pure `extract`/`model`/`wait` packages take ctx but still do no IO.

## Conventions

- **Commits**: gitmoji-driven; the leading `:code:` is the type and drives the
  release semver (`:sparkles:`→minor, `:bug:`/`:zap:`→patch, `:boom:`/`!`→major,
  `:memo:`/`:wrench:`→no bump). Subject and body in English. See
  <https://github.com/akira-toriyama/.github/blob/main/CONTRIBUTING.md>.
- **Go**: the go.mod floor is a supported minor (never an EOL pin); CI resolves
  the toolchain via `go-version-file: go.mod`. Run `sh scripts/check.sh` before a
  PR — it mirrors CI (build, vet, `go test -race`, golangci-lint, govulncheck).
- **Fleet-managed files** (`.github/workflows/commit-lint.yml`, `taplo.yml`,
  `task-status.yml`, `.github/dependabot.yml`) are distributed by fleet-sync from
  `akira-toriyama/.github`. Do NOT hand-edit per-repo copies — they get
  overwritten. In particular, `dependabot.yml` is actions-only upstream; a `gomod`
  entry belongs in the canonical template, not here.
- **Actions are SHA-pinned** with a `# vX.Y.Z` comment so Dependabot can follow.

## Release

Tag-driven: compute the next tag with `glyph bump --since-tag`, then push
`vX.Y.Z` → `release.yml` renders the notes with `glyph notes` and runs GoReleaser
(binaries, checksums, Homebrew cask to `akira-toriyama/homebrew-tap`) and
attaches a build provenance attestation. Version/commit/date are stamped via ldflags; a
source/nix build reports `dev` + the commit (never a hardcoded release string).
