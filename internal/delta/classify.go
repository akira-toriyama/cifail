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

// usesRe extracts the action ref from a live `uses:` step declaration on a diff
// line. It anchors structurally: after the +/- diff marker only whitespace and
// an optional YAML list dash (`- `) may precede the `uses:` key, so a
// commented-out `# uses:` line — or a `uses:` mentioned inside another field's
// value or comment — is rejected instead of fabricating a version bump. The
// optional quotes let `uses: "actions/checkout@v4"` match too; the ref stays in
// capture group 1. Known v1 limits (a line-only regex can't see YAML structure):
// a `run: |` block-scalar line whose text starts with `uses:` still matches, and
// the same action bumped twice in one patch reports only its first old→new pair
// (see usesChanged's name-keyed maps).
var usesRe = regexp.MustCompile(`^[+-]\s*(?:-\s+)?uses:\s*['"]?([^\s#'"]+)['"]?`)

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
