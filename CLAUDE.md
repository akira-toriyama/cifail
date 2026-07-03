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
                        annotations, log archive, runs-for-sha. IO lives here and
                        ONLY here.
internal/wait         → PURE poll/aggregate/exit logic for the `wait` subcommand;
                        a gh Poller + a Clock are injected (no IO here)
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

## Conventions

- **Commits**: gitmoji + Conventional Commits; the Conventional *type* drives the
  release (feat→minor, fix/perf→patch, `!`/BREAKING→major, docs/chore→no bump).
  Subject and body in English. See
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

Tag-driven: push `vX.Y.Z` → `release.yml` runs git-cliff + GoReleaser (binaries,
checksums, Homebrew cask to `akira-toriyama/homebrew-tap`) and attaches a build
provenance attestation. Version/commit/date are stamped via ldflags; a
source/nix build reports `dev` + the commit (never a hardcoded release string).
