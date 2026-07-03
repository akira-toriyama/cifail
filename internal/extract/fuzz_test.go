package extract

import "testing"

// FuzzExtract feeds arbitrary log bytes (CI logs are semi-trusted input) plus a
// fuzzed budget/context through the extract pipeline and asserts its load-bearing
// invariants: it never panics, never spends more than the budget, and emits a
// consistent, in-order set of excerpts. This guards the core promise — bounded,
// well-formed output — against any input.
func FuzzExtract(f *testing.F) {
	f.Add([]byte("2026-07-03T00:00:00.0000000Z --- FAIL: TestX\ncontext line\n##[error]boom\n"), 64, 2)
	f.Add([]byte(""), 0, 0)
	f.Add([]byte("no trailing newline"), 8192, 3)
	f.Add([]byte("\n\n\n"), 4, 1)
	f.Add([]byte("panic: nil deref\ngoroutine 1\n"), 40, 5)

	f.Fuzz(func(t *testing.T, raw []byte, budget, context int) {
		// The CLI validates --budget-bytes>0 and --context>=0, so negative/huge
		// values are not reachable in production; bound the fuzzed knobs to the
		// meaningful range rather than exercising nonsensical config.
		budget = boundedInt(budget, 0, 1<<16)
		context = boundedInt(context, 0, 64)
		cfg := Default()
		cfg.BudgetBytes = budget
		cfg.Context = context

		outs, agg := Extract([]StepInput{{Number: 1, Name: "s", Log: string(raw)}}, cfg)

		// The aggregate never overspends and never accounts negatively.
		if agg.UsedBytes > budget {
			t.Fatalf("aggregate UsedBytes %d > budget %d", agg.UsedBytes, budget)
		}
		if agg.UsedBytes < 0 || agg.OmittedLines < 0 {
			t.Fatalf("negative budget accounting: %+v", agg)
		}

		o := outs[0]
		n := len(splitLog(string(raw)))
		kept, keptBytes, prevEnd := 0, 0, 0
		for _, e := range o.Excerpts {
			if e.StartLine < 1 || e.StartLine > n {
				t.Fatalf("excerpt StartLine %d out of [1,%d] (n=%d)", e.StartLine, n, n)
			}
			if e.StartLine <= prevEnd {
				t.Fatalf("excerpts out of order/overlap: StartLine %d <= prev end %d", e.StartLine, prevEnd)
			}
			if len(e.Lines) == 0 {
				t.Fatalf("empty excerpt block at StartLine %d", e.StartLine)
			}
			for _, ln := range e.Lines {
				keptBytes += len(ln) + 1
			}
			kept += len(e.Lines)
			prevEnd = e.StartLine + len(e.Lines) - 1
		}
		if kept+o.OmittedLines != n {
			t.Fatalf("kept %d + omitted %d != total %d", kept, o.OmittedLines, n)
		}
		if o.UsedBytes != keptBytes {
			t.Fatalf("UsedBytes %d != summed kept bytes %d", o.UsedBytes, keptBytes)
		}
	})
}

// boundedInt maps any int (incl. negative / MinInt) into [lo, hi] without
// overflow, so the fuzzer's raw ints become sane config values.
func boundedInt(v, lo, hi int) int {
	if hi <= lo {
		return lo
	}
	span := hi - lo + 1
	m := v % span
	if m < 0 {
		m += span
	}
	return lo + m
}
