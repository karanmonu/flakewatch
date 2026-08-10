package pricing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRates(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rates.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadOverridesLowercasesLabels(t *testing.T) {
	ov, err := LoadOverrides(writeRates(t, `{"Self-Hosted-GPU": 0.42}`))
	if err != nil {
		t.Fatal(err)
	}
	// The file is written by a human reading their own cloud console, where
	// labels are often title-cased. Job labels come back from the API in
	// whatever case the workflow author used. Matching has to survive both.
	if got := ov["self-hosted-gpu"]; got != 0.42 {
		t.Fatalf("want 0.42 under the lowercased key, got %v", got)
	}
}

func TestLoadOverridesRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"negative rate":  `{"gpu": -1}`,
		"empty label":    `{"  ": 0.1}`,
		"no rates":       `{}`,
		"not an object":  `[1, 2, 3]`,
		"nested object":  `{"runners": {"gpu": 0.1}}`,
		"quoted number":  `{"gpu": "0.1"}`,
		"hourly mistake": `{"gpu": 24}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadOverrides(writeRates(t, body)); err == nil {
				t.Fatal("want an error, got none")
			}
		})
	}
}

// A rate of $24 is what you get when you type an hourly figure into a
// per-minute field, and it inflates the answer 60x. The error has to say which
// unit it wanted or the reader will just try a different number.
func TestLoadOverridesExplainsTheUnitsWhenRateIsAbsurd(t *testing.T) {
	_, err := LoadOverrides(writeRates(t, `{"gpu": 24}`))
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "per minute") {
		t.Fatalf("error should name the unit, got: %v", err)
	}
}

func TestLoadOverridesAllowsZero(t *testing.T) {
	// Zero is a legitimate answer, not a mistake: a self-hosted runner on
	// hardware that is already paid for and idle otherwise genuinely costs
	// nothing at the margin. Rejecting it would force the user to invent a
	// number to say "I know about this one and it is free".
	ov, err := LoadOverrides(writeRates(t, `{"spare-desktop": 0}`))
	if err != nil {
		t.Fatal(err)
	}
	r := ResolveWith([]string{"self-hosted", "spare-desktop"}, ov)
	if !r.Known || r.USDPerMinute != 0 || !r.UserSupplied {
		t.Fatalf("a zero override should still count as priced, got %+v", r)
	}
}

func TestResolveWithBeatsSelfHosted(t *testing.T) {
	// The point of the whole feature. Without an override these jobs vanish
	// from the total, which is the undercount that hits hardest exactly where
	// the bills are largest.
	labels := []string{"self-hosted", "linux", "gpu-large"}

	if r := Resolve(labels); r.Known {
		t.Fatal("without an override a self-hosted job must stay unpriced")
	}

	r := ResolveWith(labels, Overrides{"gpu-large": 0.42})
	if !r.Known || !r.UserSupplied {
		t.Fatalf("want a known, user-supplied rate, got %+v", r)
	}
	if r.USDPerMinute != 0.42 {
		t.Fatalf("want 0.42, got %v", r.USDPerMinute)
	}
	if r.SelfHosted {
		t.Fatal("a priced runner should not also report as an unpriced self-hosted one")
	}
}

func TestResolveWithBeatsPublishedRate(t *testing.T) {
	// An enterprise agreement is not list price, and someone who knows their
	// negotiated rate has better information than the published table.
	r := ResolveWith([]string{"ubuntu-latest"}, Overrides{"ubuntu-latest": 0.003})
	if r.USDPerMinute != 0.003 || !r.UserSupplied {
		t.Fatalf("override should win over the published rate, got %+v", r)
	}
}

func TestResolveWithPicksTheMostSpecificLabel(t *testing.T) {
	// A job carries several labels and more than one can be in the file.
	// "linux" describes a family; "gpu-large-a100" describes a machine. The
	// longer label is the more specific claim, and the result must not depend
	// on which one map iteration happened to reach first.
	ov := Overrides{"linux": 0.01, "gpu-large-a100": 0.42}
	for i := 0; i < 20; i++ {
		r := ResolveWith([]string{"self-hosted", "linux", "gpu-large-a100"}, ov)
		if r.USDPerMinute != 0.42 {
			t.Fatalf("want the most specific label to win, got %+v", r)
		}
	}
}

func TestResolveWithNilOverridesMatchesResolve(t *testing.T) {
	for _, labels := range [][]string{
		{"ubuntu-latest"},
		{"macos-14"},
		{"self-hosted", "linux"},
		{"something-nobody-has-heard-of"},
	} {
		a, b := Resolve(labels), ResolveWith(labels, nil)
		if a != b {
			t.Fatalf("%v: Resolve gave %+v, ResolveWith(nil) gave %+v", labels, a, b)
		}
	}
}
