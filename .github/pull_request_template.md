<!-- 1 item = 1 PR (squash). Keep the docs in sync in the SAME PR. -->

## What & why

<!-- The change in one or two sentences, and the reason. Link the task id this
     closes, if any. -->

## Verification

- [ ] `go build ./...`
- [ ] `go test -race ./...`
- [ ] `go vet ./...` + `golangci-lint run`
- [ ] `sh scripts/check.sh`
- [ ] Docs updated in this PR (don't leave unfinished work implicit)

## Notes

<!-- Anything not done yet, follow-ups, or risks — state them explicitly. -->
