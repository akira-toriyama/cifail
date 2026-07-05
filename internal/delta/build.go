package delta

import (
	"encoding/json"
	"strings"

	"github.com/akira-toriyama/cifail/internal/model"
)

// Build turns gathered Evidence into the delta report. It is a total function:
// every degrade (no green baseline, expired logs, failed comparison) still
// yields a report — the agent branches on the JSON fields, and Note explains
// what is missing. All IO happened before this call.
func Build(ev Evidence, cfg Config) model.DeltaReport {
	// A negative budget would break the used<=limit invariant this function
	// documents; clamp so Build guarantees it for ANY input (the CLI also
	// rejects <=0, but the pure function must not depend on its caller).
	limit := cfg.BudgetBytes
	if limit < 0 {
		limit = 0
	}
	r := model.DeltaReport{
		Failing: model.DeltaRun{Run: ev.Failing.ID, SHA: ev.Failing.SHA, Attempt: ev.Failing.Attempt, URL: ev.Failing.URL},
		Jobs:    model.DeltaJobs{NewlyFailing: []string{}},
		Budget:  model.DeltaBudget{LimitBytes: limit},
	}
	var notes []string
	if ev.CompareNote != "" {
		notes = append(notes, ev.CompareNote)
	}
	if ev.EnvNote != "" {
		notes = append(notes, ev.EnvNote)
	}

	if ev.Green == nil {
		notes = append([]string{"no green run of this workflow found on the branch (within log retention); nothing to diff against"}, notes...)
		r.Note = strings.Join(notes, "; ")
		return r
	}

	g := model.DeltaRun{Run: ev.Green.ID, SHA: ev.Green.SHA, URL: ev.Green.URL}
	if !ev.Green.StartedAt.IsZero() && ev.Now.After(ev.Green.StartedAt) {
		g.AgeDays = int(ev.Now.Sub(ev.Green.StartedAt).Hours() / 24)
	}
	r.LastGreen = &g
	r.SameCommit = ev.Green.SHA == ev.Failing.SHA

	// Zero-commit pivot: same sha means git has no answer — omit commit_range
	// entirely and point the note at the environment. This is the case agents
	// misdiagnose most.
	if r.SameCommit {
		notes = append([]string{"failing and last-green runs share the same commit; the change is in the environment (actions, runner, external deps), not the code"}, notes...)
	} else if ev.Compare != nil {
		r.CommitRange = buildCommitRange(ev.Compare)
	}

	r.Environment = buildEnvironment(ev.FailingSetup, ev.GreenSetup)
	r.Jobs.NewlyFailing = newlyFailing(ev.FailingJobs, ev.GreenJobs)

	applyBudget(&r, limit)
	r.Note = strings.Join(notes, "; ")
	return r
}

// buildEnvironment compares the two runs' resolved action SHAs and runner
// image. It needs BOTH sides — comparing against nothing would fabricate
// drift — so either side missing yields nil (the Evidence's EnvNote explains).
func buildEnvironment(failing, green []SetupLog) *model.DeltaEnvironment {
	if len(failing) == 0 || len(green) == 0 {
		return nil
	}
	fActions, fOrder, fRunner := mergeSetups(failing)
	gActions, gOrder, gRunner := mergeSetups(green)

	order := append([]string{}, gOrder...)
	for _, ref := range fOrder {
		if _, ok := gActions[ref]; !ok {
			order = append(order, ref)
		}
	}
	env := &model.DeltaEnvironment{Actions: make([]model.DeltaActionDrift, 0, len(order))}
	for _, ref := range order {
		gs, fs := gActions[ref], fActions[ref]
		env.Actions = append(env.Actions, model.DeltaActionDrift{
			Ref: ref, GreenSHA: gs, FailingSHA: fs,
			// Drift needs proof on both sides; a ref seen on one side only is
			// absence of evidence, not drift.
			Drifted: gs != "" && fs != "" && gs != fs,
		})
	}
	if fRunner != "" && gRunner != "" {
		env.Runner = &model.DeltaRunnerDrift{Green: gRunner, Failing: fRunner, Drifted: gRunner != fRunner}
	}
	return env
}

// mergeSetups folds per-job setups into one ref→sha view (first sighting wins;
// within one run the same ref resolves identically) plus the first runner id.
func mergeSetups(logs []SetupLog) (actions map[string]string, order []string, runner string) {
	actions = map[string]string{}
	for _, l := range logs {
		s := ParseSetup(l.Text)
		for _, a := range s.Actions {
			if _, ok := actions[a.Ref]; !ok {
				actions[a.Ref] = a.SHA
				order = append(order, a.Ref)
			}
		}
		if runner == "" {
			runner = s.Runner
		}
	}
	return actions, order, runner
}

// newlyFailing lists jobs failing now whose same-named job PASSED in the green
// run (skipped/cancelled greens don't count — no pass to regress from).
// Deduped, in failing-run order, always non-nil.
func newlyFailing(failing, green []JobOutcome) []string {
	passed := map[string]bool{}
	for _, j := range green {
		if j.Conclusion == "success" {
			passed[j.Name] = true
		}
	}
	out := make([]string, 0)
	seen := map[string]bool{}
	for _, j := range failing {
		if j.Conclusion == "failure" && passed[j.Name] && !seen[j.Name] {
			seen[j.Name] = true
			out = append(out, j.Name)
		}
	}
	return out
}

// budget charges serialized item sizes against a hard limit; a dropped item is
// counted, never silently lost.
type budget struct {
	limit, used, omitted int
}

func (b *budget) fits(item any) bool {
	n := 64 // unreachable fallback: all our shapes marshal
	if raw, err := json.Marshal(item); err == nil {
		n = len(raw) + 1
	}
	if b.used+n > b.limit {
		b.omitted++
		return false
	}
	b.used += n
	return true
}

// applyBudget enforces the byte budget over the report's variable-length lists
// only (the fixed envelope always fits), keeping the highest-signal content
// first: drifted actions → runner → workflow changes → lockfiles → newly
// failing jobs → top dirs → non-drifted actions. Truncation is structural —
// shorter lists plus OmittedItems — never inline marker strings.
func applyBudget(r *model.DeltaReport, limit int) {
	b := &budget{limit: limit}

	var drifted, stable []model.DeltaActionDrift
	if r.Environment != nil {
		for _, a := range r.Environment.Actions {
			if a.Drifted {
				drifted = append(drifted, a)
			} else {
				stable = append(stable, a)
			}
		}
	}

	keptDrifted := make([]model.DeltaActionDrift, 0, len(drifted))
	for _, a := range drifted {
		if b.fits(a) {
			keptDrifted = append(keptDrifted, a)
		}
	}
	if r.Environment != nil && r.Environment.Runner != nil && !b.fits(*r.Environment.Runner) {
		r.Environment.Runner = nil
	}
	if r.CommitRange != nil {
		wf := make([]model.DeltaWorkflowChange, 0, len(r.CommitRange.WorkflowChanges))
		for _, w := range r.CommitRange.WorkflowChanges {
			if b.fits(w) {
				wf = append(wf, w)
			}
		}
		r.CommitRange.WorkflowChanges = wf
		lf := make([]model.DeltaLockfile, 0, len(r.CommitRange.Lockfiles))
		for _, l := range r.CommitRange.Lockfiles {
			if b.fits(l) {
				lf = append(lf, l)
			}
		}
		r.CommitRange.Lockfiles = lf
	}
	nf := make([]string, 0, len(r.Jobs.NewlyFailing))
	for _, j := range r.Jobs.NewlyFailing {
		if b.fits(j) {
			nf = append(nf, j)
		}
	}
	r.Jobs.NewlyFailing = nf
	if r.CommitRange != nil {
		td := make([]string, 0, len(r.CommitRange.TopDirs))
		for _, d := range r.CommitRange.TopDirs {
			if b.fits(d) {
				td = append(td, d)
			}
		}
		r.CommitRange.TopDirs = td
	}
	keptStable := make([]model.DeltaActionDrift, 0, len(stable))
	for _, a := range stable {
		if b.fits(a) {
			keptStable = append(keptStable, a)
		}
	}
	if r.Environment != nil {
		r.Environment.Actions = append(keptDrifted, keptStable...)
	}

	r.Budget = model.DeltaBudget{LimitBytes: limit, UsedBytes: b.used, OmittedItems: b.omitted}
}
