package cli

import (
	"bytes"
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
	defer resetWaitFlags()
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
	defer resetWaitFlags()
	resetWaitFlags()
	waitSHA = "1c27b08" // short sha — GitHub's head_sha filter needs the full 40
	err := runWait(nil, nil)
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != core.CodeUsage {
		t.Fatalf("short --sha must be a usage error, got %v", err)
	}
}

func resetWaitFlags() {
	waitBudget, waitContext = 4096, 3
	waitTimeout, waitInterval = time.Minute, time.Second
	waitSHA = ""
}
