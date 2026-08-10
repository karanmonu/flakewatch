package main

import (
	"testing"
	"time"
)

// Go's own duration parser stops at hours, so "30d" -- the unit anyone
// describing a CI window actually reaches for -- is a parse error there.
func TestParseWindowAcceptsTheUnitsPeopleType(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"30d", 30 * 24 * time.Hour},
		{"1D", 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
		{"1W", 7 * 24 * time.Hour},
		{"48h", 48 * time.Hour},
		{"90m", 90 * time.Minute},
		{"1.5d", 36 * time.Hour},
		{" 7d ", 7 * 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseWindow(tt.in)
			if err != nil {
				t.Fatalf("parseWindow(%q) returned %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("parseWindow(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// A window that silently parses to zero would fetch the whole run history under
// the impression it was scoped, which is the opposite of what was asked.
func TestParseWindowRejectsNonsense(t *testing.T) {
	for _, in := range []string{"", "  ", "d", "0d", "-5d", "-1h", "30 days", "soon", "w"} {
		t.Run(in, func(t *testing.T) {
			if got, err := parseWindow(in); err == nil {
				t.Errorf("parseWindow(%q) = %v, want an error", in, got)
			}
		})
	}
}

func TestSplitPathsDropsEmptyEntries(t *testing.T) {
	got := splitPaths(" .github/workflows/ci.yml , ,.github/workflows/release.yml,")
	want := []string{".github/workflows/ci.yml", ".github/workflows/release.yml"}

	if len(got) != len(want) {
		t.Fatalf("got %d paths %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("path %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// An unset shell variable arrives as the empty string. It must mean "no paths",
// not "one path that is the empty string", which would match nothing and mark
// nothing while looking like it worked.
func TestSplitPathsOfEmptyStringIsEmpty(t *testing.T) {
	if got := splitPaths(""); len(got) != 0 {
		t.Errorf("splitPaths(\"\") = %q, want nothing", got)
	}
}
