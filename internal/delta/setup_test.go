package delta

import (
	"reflect"
	"testing"
)

// A realistic 'Set up job' step log: every archive line carries an RFC3339Nano
// timestamp prefix that must be stripped before matching.
const setupFixture = `2026-07-01T03:04:05.1234567Z Current runner version: '2.325.0'
2026-07-01T03:04:05.1234567Z ##[group]Operating System
2026-07-01T03:04:05.1234567Z Ubuntu
2026-07-01T03:04:05.1234567Z 24.04.2
2026-07-01T03:04:05.1234567Z LTS
2026-07-01T03:04:05.1234567Z ##[endgroup]
2026-07-01T03:04:05.1234567Z ##[group]Runner Image
2026-07-01T03:04:05.1234567Z Image: ubuntu-24.04
2026-07-01T03:04:05.1234567Z Version: 20250601.1.0
2026-07-01T03:04:05.1234567Z Included Software: https://example.test/sw
2026-07-01T03:04:05.1234567Z ##[endgroup]
2026-07-01T03:04:06.1234567Z Download action repository 'actions/checkout@v4' (SHA:11bd71901bbe5b1630ceea73d27597364c9af683)
2026-07-01T03:04:07.1234567Z Download action repository 'actions/setup-go@v5' (SHA:d35c59abb061a4a6fb18e82ac0862c26744d6ab5)
2026-07-01T03:04:08.1234567Z Complete job name: test`

func TestParseSetup(t *testing.T) {
	s := ParseSetup(setupFixture)
	wantActions := []ActionResolution{
		{Ref: "actions/checkout@v4", SHA: "11bd71901bbe5b1630ceea73d27597364c9af683"},
		{Ref: "actions/setup-go@v5", SHA: "d35c59abb061a4a6fb18e82ac0862c26744d6ab5"},
	}
	if !reflect.DeepEqual(s.Actions, wantActions) {
		t.Errorf("Actions = %+v, want %+v", s.Actions, wantActions)
	}
	if s.Runner != "ubuntu-24.04/20250601.1.0" {
		t.Errorf("Runner = %q, want ubuntu-24.04/20250601.1.0", s.Runner)
	}
}

// Version: must only count AFTER Image: — a stray Version: line earlier in the
// log (e.g. runner version) must not be mistaken for the image version.
func TestParseSetupVersionRequiresImageFirst(t *testing.T) {
	s := ParseSetup("2026-07-01T03:04:05Z Version: 9.9.9\n2026-07-01T03:04:05Z Image: ubuntu-22.04\n2026-07-01T03:04:05Z Version: 20250101.2.0")
	if s.Runner != "ubuntu-22.04/20250101.2.0" {
		t.Errorf("Runner = %q, want ubuntu-22.04/20250101.2.0", s.Runner)
	}
}

func TestParseSetupEmptyAndGarbage(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"empty", ""},
		{"no matches", "2026-07-01T03:04:05Z hello\nworld"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ParseSetup(tc.in)
			if len(s.Actions) != 0 || s.Runner != "" {
				t.Errorf("ParseSetup(%q) = %+v, want empty", tc.in, s)
			}
		})
	}
}

func TestDefault(t *testing.T) {
	// Pins the default to the literal 4096: a future DefaultBudgetBytes change
	// must consciously update this test. (The brief's `got != DefaultBudgetBytes
	// || got != 4096` is rejected by go vet's bools check — "suspect or" — which
	// `go test` runs by default; since got is Default().BudgetBytes ==
	// DefaultBudgetBytes by construction, this reduced form asserts the same.)
	if got := Default().BudgetBytes; got != 4096 {
		t.Errorf("Default().BudgetBytes = %d, want 4096", got)
	}
}

// The parser sees arbitrary log bytes (truncated archives, binary junk); it
// must never panic and never emit half-empty resolutions.
func FuzzParseSetup(f *testing.F) {
	f.Add(setupFixture)
	f.Add("")
	f.Add("2026-07-01T00:00:00Z Download action repository '' (SHA:)")
	f.Fuzz(func(t *testing.T, text string) {
		s := ParseSetup(text)
		for _, a := range s.Actions {
			if a.Ref == "" || a.SHA == "" {
				t.Fatalf("empty field in %+v", a)
			}
		}
	})
}
