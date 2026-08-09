// Package gh talks to the GitHub REST API for cifail: it reuses the user's `gh`
// CLI token (no separate auth or per-repo config), resolves a PR/branch/run to
// its latest failing workflow run, and downloads the run log archive once to
// pull out the failed steps. It returns raw data (run metadata, failed jobs and
// steps, per-step log text, annotations); the extract package does the paring.
package gh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/akira-toriyama/cifail/internal/core"
)

const (
	apiBase     = "https://api.github.com"
	apiVersion  = "2022-11-28"
	userAgent   = "cifail"
	httpTimeout = 30 * time.Second
)

// Client is an authenticated GitHub API client scoped to one repository.
type Client struct {
	Owner string
	Repo  string
	token string
	http  *http.Client
	base  string
}

// NewClient builds a client for owner/repo, reusing the `gh` CLI's token. The
// token is fetched via `gh auth token` (cancellable via ctx), so cifail needs no
// auth of its own.
func NewClient(ctx context.Context, owner, repo string) (*Client, error) {
	token, err := ghAuthToken(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{
		Owner: owner,
		Repo:  repo,
		token: token,
		http:  &http.Client{Timeout: httpTimeout},
		base:  apiBase,
	}, nil
}

// ghAuthToken shells out to `gh auth token` to reuse the user's existing
// GitHub authentication. exec.CommandContext ties the child to ctx so a Ctrl-C
// kills it instead of orphaning it.
func ghAuthToken(ctx context.Context) (string, error) {
	// #nosec G204 -- fixed argv (`gh auth token`); no user input reaches the argv.
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return "", core.APIf("could not get a GitHub token from `gh auth token` (is the gh CLI installed and authenticated? run `gh auth login`): %v", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", core.APIf("`gh auth token` returned an empty token; run `gh auth login`")
	}
	return token, nil
}

// newRequest builds an authenticated GET for an API path (absolute, starting
// with "/") or a full URL, bound to ctx so the request is cancellable.
func (c *Client) newRequest(ctx context.Context, pathOrURL string) (*http.Request, error) {
	url := pathOrURL
	if strings.HasPrefix(pathOrURL, "/") {
		url = c.base + pathOrURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, core.APIf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	return req, nil
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	req, err := c.newRequest(ctx, path)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return core.APIf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return c.statusError(path, resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return core.APIf("decode %s: %v", path, err)
	}
	return nil
}

// getRaw GETs a path (following redirects, e.g. the log archive's signed URL)
// and returns the raw body bytes.
func (c *Client) getRaw(ctx context.Context, path string) ([]byte, error) {
	req, err := c.newRequest(ctx, path)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, core.APIf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, c.statusError(path, resp)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, core.APIf("read %s: %v", path, err)
	}
	return b, nil
}

// statusError turns a non-200 response into a typed error, mapping auth/rate
// problems to actionable messages.
func (c *Client) statusError(path string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	msg := strings.TrimSpace(string(body))
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return core.APIf("GitHub rate limit exceeded (GET %s); retry after reset", path)
		}
		return core.APIf("GitHub denied GET %s (%d) — token may lack access to %s/%s: %s", path, resp.StatusCode, c.Owner, c.Repo, msg)
	case http.StatusNotFound:
		return core.APIf("not found: GET %s (%d)", path, resp.StatusCode)
	default:
		return core.APIf("GitHub GET %s failed (%d): %s", path, resp.StatusCode, msg)
	}
}

func (c *Client) repoPath(suffix string) string {
	return fmt.Sprintf("/repos/%s/%s%s", c.Owner, c.Repo, suffix)
}
