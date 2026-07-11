package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/akira-toriyama/cifail/internal/core"
)

func TestFinishSilentSkipsStderr(t *testing.T) {
	var buf bytes.Buffer
	old := errOut
	errOut = &buf
	defer func() { errOut = old }()

	code := finish(&core.Error{Code: core.CodeNotConcluded, Silent: true, Msg: "x"})
	if code != 124 {
		t.Errorf("code = %d, want 124", code)
	}
	if buf.Len() != 0 {
		t.Errorf("silent error must not write stderr, got %q", buf.String())
	}
}

func TestFinishNonSilentWritesEnvelope(t *testing.T) {
	var buf bytes.Buffer
	old := errOut
	errOut = &buf
	defer func() { errOut = old }()

	code := finish(&core.Error{Code: core.CodeAPI, Msg: "boom"})
	if code != 3 {
		t.Errorf("code = %d, want 3", code)
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Errorf("want error envelope, got %q", buf.String())
	}
}

func TestRunWaitRejectsBadFlags(t *testing.T) {
	withWaitFlags(t)
	cases := []struct {
		name              string
		budget, ctx       int
		timeout, interval time.Duration
	}{
		{"budget", 0, 0, time.Minute, time.Second},
		{"context", 8192, -1, time.Minute, time.Second},
		{"timeout", 8192, 0, 0, time.Second},
		{"interval", 8192, 0, time.Minute, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			waitBudget, waitContext, waitTimeout, waitInterval = tc.budget, tc.ctx, tc.timeout, tc.interval
			err := runWait(nil, nil)
			var ce *core.Error
			if !errors.As(err, &ce) || ce.Code != core.CodeUsage {
				t.Fatalf("want usage error, got %v", err)
			}
		})
	}
}

func TestRunWaitRejectsShortSHA(t *testing.T) {
	withWaitFlags(t)
	waitSHA = "1c27b08" // short sha — GitHub's head_sha filter needs the full 40
	err := runWait(nil, nil)
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != core.CodeUsage {
		t.Fatalf("short --sha must be a usage error, got %v", err)
	}
}

// GitHub's head_sha filter matches only the lowercase sha, so a valid uppercase
// one must be accepted AND canonicalized — else it silently reads as no_runs.
func TestNormalizeSHA(t *testing.T) {
	const lower = "1c27b08e9d3a4f5061728394a5b6c7d8e9f00112"
	upper := strings.ToUpper(lower)
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{"lowercase passthrough", lower, lower, true},
		{"uppercase canonicalized", upper, lower, true},
		{"mixed case canonicalized", "1C27b08E9d3a4f5061728394a5b6c7d8e9f00112", lower, true},
		{"short rejected", "1c27b08", "", false},
		{"too long rejected", lower + "a", "", false},
		{"non-hex rejected", "z" + lower[1:], "", false},
		{"empty rejected", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeSHA(tc.in)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("normalizeSHA(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestInterruptOr(t *testing.T) {
	boom := core.APIf("boom")
	// Live ctx: the underlying error passes through untouched.
	if got := interruptOr(context.Background(), boom); !errors.Is(got, boom) {
		t.Errorf("live ctx: got %v, want the original error", got)
	}
	// Cancelled ctx: the error is replaced by a silent 130 interrupt.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := interruptOr(ctx, boom)
	var ce *core.Error
	if !errors.As(got, &ce) || ce.Code != core.CodeInterrupted || !ce.Silent {
		t.Errorf("cancelled ctx: got %v, want a silent CodeInterrupted", got)
	}
}

func TestRealClockSleepCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A minute-long sleep on a cancelled ctx must return ~immediately with the
	// ctx error, not block — this is what makes `wait` interruptible.
	if err := (realClock{}).Sleep(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("Sleep on cancelled ctx = %v, want context.Canceled", err)
	}
}

// withWaitFlags seeds valid wait flags and registers their reset as a t.Cleanup,
// composing teardown through a helper instead of a per-test defer.
func withWaitFlags(t *testing.T) {
	t.Helper()
	resetWaitFlags()
	t.Cleanup(resetWaitFlags)
}

func resetWaitFlags() {
	waitBudget, waitContext = 4096, 3
	waitTimeout, waitInterval = time.Minute, time.Second
	waitSHA = ""
}
