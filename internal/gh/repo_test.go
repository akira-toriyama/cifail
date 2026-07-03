package gh

import "testing"

func TestParseRemoteURL(t *testing.T) {
	cases := []struct {
		url, owner, repo string
		ok               bool
	}{
		{"https://github.com/akira-toriyama/cifail.git", "akira-toriyama", "cifail", true},
		{"https://github.com/akira-toriyama/cifail", "akira-toriyama", "cifail", true},
		{"git@github.com:akira-toriyama/cifail.git", "akira-toriyama", "cifail", true},
		{"ssh://git@github.com/akira-toriyama/cifail.git", "akira-toriyama", "cifail", true},
		{"https://github.com/akira-toriyama/cifail/", "akira-toriyama", "cifail", true},
		{"https://gitlab.com/foo/bar.git", "", "", false},
		{"not a url", "", "", false},
	}
	for _, c := range cases {
		owner, repo, ok := parseRemoteURL(c.url)
		if ok != c.ok || owner != c.owner || repo != c.repo {
			t.Errorf("parseRemoteURL(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.url, owner, repo, ok, c.owner, c.repo, c.ok)
		}
	}
}

func TestParseRepoSpec(t *testing.T) {
	owner, repo, err := parseRepoSpec("akira-toriyama/cifail")
	if err != nil || owner != "akira-toriyama" || repo != "cifail" {
		t.Errorf("parseRepoSpec ok case = (%q, %q, %v)", owner, repo, err)
	}
	for _, bad := range []string{"", "justone", "a/b/c", "/repo", "owner/"} {
		if _, _, err := parseRepoSpec(bad); err == nil {
			t.Errorf("parseRepoSpec(%q) should have errored", bad)
		}
	}
}
