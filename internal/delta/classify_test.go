package delta

import (
	"reflect"
	"testing"
)

func TestBuildCommitRange(t *testing.T) {
	cmp := &Comparison{
		AheadBy: 3, BehindBy: 1, Capped: false,
		Files: []FileChange{
			{Path: "src/api/a.go", Additions: 5, Deletions: 1},
			{Path: "src/api/b.go", Additions: 2, Deletions: 0},
			{Path: "go.sum", Additions: 14, Deletions: 9},
			{Path: "README.md", Additions: 1, Deletions: 1},
			{Path: ".github/workflows/ci.yml", Additions: 1, Deletions: 1,
				Patch: "@@ -10 +10 @@\n-      - uses: actions/setup-node@v4\n+      - uses: actions/setup-node@v5"},
		},
	}
	cr := buildCommitRange(cmp)
	if cr.AheadBy != 3 || cr.BehindBy != 1 || cr.FilesChanged != 5 {
		t.Errorf("counts = %+v, want ahead 3 / behind 1 / files 5", cr)
	}
	// Largest dir first; ties by name ascending ("." sorts before "src/api");
	// root files under ".".
	wantDirs := []string{". (2)", "src/api (2)", ".github/workflows (1)"}
	if !reflect.DeepEqual(cr.TopDirs, wantDirs) {
		t.Errorf("TopDirs = %v, want %v", cr.TopDirs, wantDirs)
	}
	if len(cr.Lockfiles) != 1 || cr.Lockfiles[0].Path != "go.sum" || cr.Lockfiles[0].Additions != 14 {
		t.Errorf("Lockfiles = %+v, want go.sum +14", cr.Lockfiles)
	}
	if len(cr.WorkflowChanges) != 1 || cr.WorkflowChanges[0].Path != ".github/workflows/ci.yml" {
		t.Fatalf("WorkflowChanges = %+v, want ci.yml", cr.WorkflowChanges)
	}
	wantUses := []string{"actions/setup-node v4→v5"}
	if !reflect.DeepEqual(cr.WorkflowChanges[0].UsesChanged, wantUses) {
		t.Errorf("UsesChanged = %v, want %v", cr.WorkflowChanges[0].UsesChanged, wantUses)
	}
}

// A workflow patch whose uses: lines did not move (or only appear on one side)
// must yield an empty — but non-nil — uses_changed.
func TestUsesChangedNoPairs(t *testing.T) {
	got := usesChanged("@@ -1 +1 @@\n+      - uses: actions/cache@v4\n- name: x")
	if got == nil || len(got) != 0 {
		t.Errorf("usesChanged = %#v, want empty non-nil slice", got)
	}
}

// SHA-pinned refs with a trailing version comment must compare on the ref
// token only (the comment is not part of the ref).
func TestUsesChangedShaPinned(t *testing.T) {
	patch := "-        uses: actions/checkout@11bd719 # v4.2.2\n+        uses: actions/checkout@08c6903 # v5.0.0"
	want := []string{"actions/checkout 11bd719→08c6903"}
	if got := usesChanged(patch); !reflect.DeepEqual(got, want) {
		t.Errorf("usesChanged = %v, want %v", got, want)
	}
}

func TestTopDir(t *testing.T) {
	for in, want := range map[string]string{
		"main.go":               ".",
		"cmd/x.go":              "cmd",
		"internal/gh/client.go": "internal/gh",
		"a/b/c/d.go":            "a/b",
	} {
		if got := topDir(in); got != want {
			t.Errorf("topDir(%q) = %q, want %q", in, got, want)
		}
	}
}

// A commented-out or comment-mentioned `uses:` is not a live version change —
// the tightened regex must reject it instead of fabricating a bump (the bug the
// old unanchored `.*\buses:` had).
func TestUsesChangedRejectsComments(t *testing.T) {
	patch := "-        uses: actions/checkout@v4\n" +
		"+        # uses: actions/checkout@v5\n" +
		"+        - name: Checkout # uses: something/else@v9"
	if got := usesChanged(patch); len(got) != 0 {
		t.Errorf("usesChanged = %v, want empty (commented/mentioned uses: are not live changes)", got)
	}
}

// Quoted ref values (valid YAML) must be captured with the quotes stripped.
func TestUsesChangedQuotedValues(t *testing.T) {
	patch := "-        uses: \"actions/checkout@v4\"\n" +
		"+        uses: 'actions/checkout@v5'"
	want := []string{"actions/checkout v4→v5"}
	if got := usesChanged(patch); !reflect.DeepEqual(got, want) {
		t.Errorf("usesChanged = %v, want %v", got, want)
	}
}
