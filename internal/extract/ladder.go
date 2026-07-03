package extract

import "regexp"

// The severity ladder ranks a log line by how strongly it points at the root
// cause of a CI failure. GitHub's own problem matchers are unreliable here (the
// setup-go matcher misses `file.go:15: msg` test failures, swift has none), so
// cifail carries its own ordered patterns. Higher tier = keep first when the
// budget is tight. The tiers are, low to high: soft signals, generic errors,
// test/build failures, crashes/fatal.
//
// These deliberately over-match rather than under-match: a false positive costs
// one kept context line, a false negative drops the actual error.
var ladderTiers = []*regexp.Regexp{
	// tier 1 — soft signals worth keeping only if nothing better competes.
	regexp.MustCompile(`(?i)(\bwarning\b|\bdenied\b|\bdeprecated\b|\btimed?\s?out\b|\bskipped\b)`),

	// tier 2 — generic errors, exceptions, and failures.
	regexp.MustCompile(`(?i)(\berror\b|\bexception\b|\btraceback\b|\bassert(ion)?\b|\bfail(ed|ure|s)?\b|\bunexpected\b|\bnot ok\b)`),

	// tier 3 — explicit test/build failure markers.
	regexp.MustCompile(`(?i)(--- FAIL|\bFAILED\b|✘|✗|\bcannot find\b|undefined[: ]|undefined (reference|symbol)|compilation (failed|error)|build (failed|error))`),

	// tier 4 — crashes, fatal errors, and the runner's own error marker.
	regexp.MustCompile(`(?i)(panic:|fatal:|fatal error|runtime error:|segmentation fault|sigsegv|sigabrt|##\[error\])`),
}

// Severity returns the highest ladder tier a line matches (1..len(ladderTiers)),
// or 0 when the line matches nothing. A higher number is a stronger signal.
func Severity(line string) int {
	for tier := len(ladderTiers); tier >= 1; tier-- {
		if ladderTiers[tier-1].MatchString(line) {
			return tier
		}
	}
	return 0
}
