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
