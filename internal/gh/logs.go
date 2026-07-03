package gh

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/akira-toriyama/cifail/internal/core"
)

// The run log archive lays logs out as, per observation and cli/cli's source:
//   - "<ordinal>_<jobName>.txt"          — the whole job log (always present)
//   - "<jobName>/<stepNumber>_<step>.txt" — one file per step (not always present)
//   - "<jobName>/system.txt"             — runner diagnostics (ignored)
//
// Job/step names are sanitized in paths (separators swapped), so cifail matches
// on a normalized key (lowercased, alphanumerics only) rather than exact names.
var (
	jobLogRe  = regexp.MustCompile(`^\d+_(.+)\.txt$`)
	stepLogRe = regexp.MustCompile(`^([^/]+)/(\d+)_(.+)\.txt$`)
	nonAlnum  = regexp.MustCompile(`[^a-z0-9]+`)
)

// LogArchive is a parsed run log archive, indexed by normalized job name.
type LogArchive struct {
	jobLogs  map[string]string         // normJob -> whole job log
	stepLogs map[string]map[int]string // normJob -> stepNumber -> step log
}

// FetchLogs downloads and parses the run's log archive (one GET, following the
// signed-URL redirect).
func (c *Client) FetchLogs(ctx context.Context, runID int64) (*LogArchive, error) {
	b, err := c.getRaw(ctx, c.repoPath(fmt.Sprintf("/actions/runs/%d/logs", runID)))
	if err != nil {
		return nil, err
	}
	return parseLogZip(b)
}

// parseLogZip builds the job/step log index from raw zip bytes.
func parseLogZip(b []byte) (*LogArchive, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, core.APIf("the run log archive is not a valid zip (%v)", err)
	}
	a := &LogArchive{
		jobLogs:  map[string]string{},
		stepLogs: map[string]map[int]string{},
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := f.Name
		if m := stepLogRe.FindStringSubmatch(name); m != nil {
			num, convErr := strconv.Atoi(m[2])
			if convErr != nil {
				continue
			}
			content, readErr := readZipFile(f)
			if readErr != nil {
				return nil, readErr
			}
			key := normalize(m[1])
			if a.stepLogs[key] == nil {
				a.stepLogs[key] = map[int]string{}
			}
			a.stepLogs[key][num] = content
			continue
		}
		if m := jobLogRe.FindStringSubmatch(name); m != nil {
			content, readErr := readZipFile(f)
			if readErr != nil {
				return nil, readErr
			}
			a.jobLogs[normalize(m[1])] = content
		}
	}
	return a, nil
}

// StepLog returns the per-step log for a job's step, if the archive has one.
func (a *LogArchive) StepLog(jobName string, stepNumber int) (string, bool) {
	steps, ok := a.stepLogs[normalize(jobName)]
	if !ok {
		return "", false
	}
	s, ok := steps[stepNumber]
	return s, ok
}

// JobLog returns the whole-job log for a job, if present.
func (a *LogArchive) JobLog(jobName string) (string, bool) {
	s, ok := a.jobLogs[normalize(jobName)]
	return s, ok
}

func readZipFile(f *zip.File) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", core.APIf("open %s in log archive: %v", f.Name, err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		return "", core.APIf("read %s in log archive: %v", f.Name, err)
	}
	return string(b), nil
}

// normalize reduces a job/step name to a match key: lowercase, alphanumerics
// only. This survives the path sanitization GitHub applies to names.
func normalize(s string) string {
	return nonAlnum.ReplaceAllString(strings.ToLower(s), "")
}
