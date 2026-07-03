package gh

import (
	"context"
	"os/exec"
	"regexp"
	"strings"

	"github.com/akira-toriyama/cifail/internal/core"
)

// ResolveRepo returns owner/repo. A non-empty spec ("owner/repo") wins;
// otherwise it is derived from the git "origin" remote of dir.
func ResolveRepo(ctx context.Context, spec, dir string) (owner, repo string, err error) {
	if spec != "" {
		return parseRepoSpec(spec)
	}
	url, gitErr := gitOutput(ctx, dir, "remote", "get-url", "origin")
	if gitErr != nil {
		return "", "", core.Usagef("no --repo given and could not read the git origin remote in %q (%v); pass --repo owner/repo", dir, gitErr)
	}
	owner, repo, ok := parseRemoteURL(url)
	if !ok {
		return "", "", core.Usagef("could not parse owner/repo from origin remote %q; pass --repo owner/repo", url)
	}
	return owner, repo, nil
}

// parseRepoSpec splits an "owner/repo" string.
func parseRepoSpec(spec string) (string, string, error) {
	spec = strings.TrimSuffix(strings.TrimSpace(spec), ".git")
	parts := strings.Split(spec, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", core.Usagef("--repo must be in owner/repo form, got %q", spec)
	}
	return parts[0], parts[1], nil
}

// remoteURLRe matches the owner/repo tail of the common GitHub remote URL forms:
// https://github.com/owner/repo(.git), git@github.com:owner/repo(.git), and
// ssh://git@github.com/owner/repo(.git).
var remoteURLRe = regexp.MustCompile(`(?:github\.com[:/])([^/]+)/(.+?)(?:\.git)?/?$`)

// parseRemoteURL extracts owner/repo from a GitHub remote URL.
func parseRemoteURL(url string) (owner, repo string, ok bool) {
	m := remoteURLRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// CurrentBranch returns the checked-out branch name in dir (git symbolic HEAD).
func CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := gitOutput(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", core.Usagef("could not determine the current branch in %q (%v); pass --branch, --pr, or --run", dir, err)
	}
	branch := strings.TrimSpace(out)
	if branch == "" || branch == "HEAD" {
		return "", core.Usagef("detached HEAD in %q; pass --branch, --pr, or --run", dir)
	}
	return branch, nil
}

// CurrentSHA returns the full HEAD commit sha in dir.
func CurrentSHA(ctx context.Context, dir string) (string, error) {
	out, err := gitOutput(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", core.Usagef("could not resolve HEAD in %q (%v); pass --sha", dir, err)
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", core.Usagef("empty HEAD in %q; pass --sha", dir)
	}
	return sha, nil
}

// gitOutput runs `git -C dir <args...>` and returns trimmed stdout. The child is
// bound to ctx (exec.CommandContext) so an interrupt kills it.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	// #nosec G204 -- fixed program (`git`) with cifail-controlled subcommands; the
	// only external value is dir (the working directory), passed as a plain argv
	// to `-C`, not through a shell.
	out, err := exec.CommandContext(ctx, "git", full...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
