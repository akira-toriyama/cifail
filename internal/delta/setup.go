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
