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
