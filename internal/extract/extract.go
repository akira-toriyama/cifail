// Package extract turns a failed step's raw CI log into a small, high-signal set
// of excerpts that fit a byte budget. It is pure (no IO): the gh layer hands it
// log text, and it applies the severity ladder plus a budget algorithm that
// keeps error lines (with context) and anchors the head and tail, biased toward
// the end of the log where the failure usually surfaces.
package extract

import (
	"regexp"
	"sort"
	"strings"

	"github.com/akira-toriyama/cifail/internal/model"
)

// Config tunes the extraction. Zero values are not meaningful; use Default and
// override fields as needed.
type Config struct {
	BudgetBytes int // total bytes of log content to keep (across all steps in Extract)
	Context     int // lines of context kept on each side of a matched error line
	Head        int // lines anchored at the start of a step log
	Tail        int // lines anchored at the end of a step log
	Floor       int // minimum head/tail lines reserved before errors compete for budget
}

// Default is the standard extraction profile: an 8 KiB budget with modest
// context and head/tail anchors.
func Default() Config {
	return Config{BudgetBytes: 8192, Context: 3, Head: 8, Tail: 12, Floor: 3}
}

// StepInput is one failed step's raw log (as returned in the run log archive).
type StepInput struct {
	Number int
	Name   string
	Log    string
}

// StepOutput is the budgeted result for one step.
type StepOutput struct {
	Number       int
	Name         string
	Excerpts     []model.Excerpt
	OmittedLines int
	UsedBytes    int
}

// tsPrefix matches the RFC3339Nano UTC timestamp GitHub prepends to every raw
// log line ("2026-07-03T00:00:00.1234567Z <content>"), so it can be stripped.
var tsPrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z\s`)

// line categories, ordered so a contiguous run takes the strongest reason.
const (
	catNone = iota
	catHead
	catTail
	catMatch
)

// Extract budgets Config.BudgetBytes across the given failed steps and returns
// a per-step result plus the aggregate budget. The LAST step gets first claim
// on the budget (tail-bias across steps: the step that killed the run matters
// most); earlier steps take what remains. Output order matches the input.
func Extract(steps []StepInput, cfg Config) ([]StepOutput, model.Budget) {
	outs := make([]StepOutput, len(steps))
	remaining := cfg.BudgetBytes
	// Walk steps back-to-front so the final failing step is budgeted first.
	for i := len(steps) - 1; i >= 0; i-- {
		out := ExtractStep(steps[i], remaining, cfg)
		remaining -= out.UsedBytes
		if remaining < 0 {
			remaining = 0
		}
		outs[i] = out
	}
	budget := model.Budget{LimitBytes: cfg.BudgetBytes}
	for _, o := range outs {
		budget.UsedBytes += o.UsedBytes
		budget.OmittedLines += o.OmittedLines
	}
	return outs, budget
}

// ExtractStep pares one step's log down to the given byte budget. The whole log
// is returned when it already fits; otherwise it reserves a floor of head/tail
// anchors, then fills the remaining budget with error blocks (severity desc,
// latest first), then expands the head/tail anchors with anything left.
func ExtractStep(step StepInput, budget int, cfg Config) StepOutput {
	lines := splitLog(step.Log)
	n := len(lines)
	out := StepOutput{Number: step.Number, Name: step.Name}
	if n == 0 {
		return out
	}

	bytesOf := func(i int) int { return len(lines[i]) + 1 } // +1 for the newline

	total := 0
	for i := range lines {
		total += bytesOf(i)
	}
	if total <= budget {
		out.Excerpts = []model.Excerpt{{StartLine: 1, Reason: "full", Lines: lines}}
		out.UsedBytes = total
		return out
	}

	keep := make([]bool, n)
	cat := make([]int, n)
	used := 0

	// add includes line i if it still fits the budget; it records the strongest
	// category seen for that line.
	add := func(i, c int) {
		if i < 0 || i >= n {
			return
		}
		if !keep[i] {
			if used+bytesOf(i) > budget {
				return
			}
			keep[i] = true
			used += bytesOf(i)
		}
		if c > cat[i] {
			cat[i] = c
		}
	}

	floor := clamp(cfg.Floor, 0, n)
	head := clamp(cfg.Head, floor, n)
	tail := clamp(cfg.Tail, floor, n)

	// 1) Reserve the head/tail FLOOR first so both ends are always anchored.
	for i := 0; i < floor; i++ {
		add(i, catHead)
	}
	for i := n - floor; i < n; i++ {
		add(i, catTail)
	}

	// 2) Fill with error blocks: highest severity first, latest first (tail-bias
	//    among equally-severe errors). Within a block, keep lines closest to the
	//    peak first so a partial fit still centers on the error.
	for _, blk := range matchBlocks(lines, cfg.Context) {
		for _, i := range blk.byImportance() {
			add(i, catMatch)
		}
	}

	// 3) Expand the anchors with whatever budget remains: head forward, tail
	//    backward (keeping the latest lines).
	for i := floor; i < head; i++ {
		add(i, catHead)
	}
	for i := n - tail; i < n-floor; i++ {
		add(i, catTail)
	}

	out.Excerpts = compose(lines, keep, cat)
	out.UsedBytes = used
	for _, k := range keep {
		if !k {
			out.OmittedLines++
		}
	}
	return out
}

// block is a contiguous window of lines around one or more matched error lines.
type block struct {
	start, end int // [start, end)
	peak       int // index of the highest-severity line in the block
	sev        int // that peak severity
}

// byImportance orders a block's line indices by proximity to its peak (closest
// first), so a partial budget still captures the error and its nearest context.
func (b block) byImportance() []int {
	idx := make([]int, 0, b.end-b.start)
	for i := b.start; i < b.end; i++ {
		idx = append(idx, i)
	}
	sort.SliceStable(idx, func(x, y int) bool {
		dx, dy := abs(idx[x]-b.peak), abs(idx[y]-b.peak)
		if dx != dy {
			return dx < dy
		}
		return idx[x] > idx[y] // ties: later line first (tail-bias)
	})
	return idx
}

// matchBlocks finds error lines via the severity ladder, expands each by context
// lines, merges overlapping windows, and returns the blocks ordered for budget
// filling: highest severity first, then latest first.
func matchBlocks(lines []string, context int) []block {
	type hit struct{ i, sev int }
	var hits []hit
	for i, l := range lines {
		if s := Severity(l); s > 0 {
			hits = append(hits, hit{i, s})
		}
	}
	if len(hits) == 0 {
		return nil
	}
	n := len(lines)
	var blocks []block
	for _, h := range hits {
		start := clamp(h.i-context, 0, n)
		end := clamp(h.i+context+1, 0, n)
		if k := len(blocks) - 1; k >= 0 && start <= blocks[k].end {
			// overlaps/adjacent: merge, tracking the strongest peak.
			if end > blocks[k].end {
				blocks[k].end = end
			}
			if h.sev >= blocks[k].sev {
				blocks[k].sev = h.sev
				blocks[k].peak = h.i
			}
			continue
		}
		blocks = append(blocks, block{start: start, end: end, peak: h.i, sev: h.sev})
	}
	sort.SliceStable(blocks, func(a, b int) bool {
		if blocks[a].sev != blocks[b].sev {
			return blocks[a].sev > blocks[b].sev // higher severity first
		}
		return blocks[a].peak > blocks[b].peak // then later first (tail-bias)
	})
	return blocks
}

// compose turns the keep set into contiguous excerpt blocks in log order, each
// tagged with the strongest reason among its lines.
func compose(lines []string, keep []bool, cat []int) []model.Excerpt {
	var excerpts []model.Excerpt
	i := 0
	for i < len(lines) {
		if !keep[i] {
			i++
			continue
		}
		start := i
		runCat := catNone
		var runLines []string
		for i < len(lines) && keep[i] {
			runLines = append(runLines, lines[i])
			if cat[i] > runCat {
				runCat = cat[i]
			}
			i++
		}
		excerpts = append(excerpts, model.Excerpt{
			StartLine: start + 1,
			Reason:    reasonName(runCat),
			Lines:     runLines,
		})
	}
	return excerpts
}

func reasonName(c int) string {
	switch c {
	case catMatch:
		return "match"
	case catTail:
		return "tail"
	default:
		return "head"
	}
}

// splitLog splits raw log text into lines and strips the leading Actions
// timestamp from each. A single trailing empty line (from a final newline) is
// dropped so it doesn't count against the budget.
func splitLog(s string) []string {
	if s == "" {
		return nil
	}
	raw := strings.Split(s, "\n")
	if len(raw) > 0 && raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}
	out := make([]string, len(raw))
	for i, l := range raw {
		out[i] = tsPrefix.ReplaceAllString(l, "")
	}
	return out
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
