package gh

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akira-toriyama/cifail/internal/core"
)

// statusError maps GitHub's non-200 responses to actionable messages. The
// discriminator between "rate limited" and "no access" on a 403 is the
// X-RateLimit-Remaining header, and every case must be a CodeAPI error.
func TestStatusError(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		rateHeader string // "" = header absent
		body       string
		wantSubstr string
	}{
		{"rate limited", http.StatusForbidden, "0", `{"message":"API rate limit exceeded"}`, "rate limit"},
		{"forbidden without rate header", http.StatusForbidden, "", `{"message":"Resource not accessible"}`, "token may lack access"},
		{"not found", http.StatusNotFound, "", `{"message":"Not Found"}`, "not found"},
		{"server error", http.StatusInternalServerError, "", "boom", "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.rateHeader != "" {
					w.Header().Set("X-RateLimit-Remaining", tc.rateHeader)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			_, err := testClient(srv).getRaw(context.Background(), "/x")
			if err == nil {
				t.Fatalf("getRaw on %d = nil error, want an API error", tc.status)
			}
			if code := core.ExitCode(err); code != int(core.CodeAPI) {
				t.Errorf("exit code = %d, want %d (CodeAPI)", code, core.CodeAPI)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}
