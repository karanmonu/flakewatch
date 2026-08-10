package pricing

import (
	"testing"
	"time"
)

func TestCeilMinutes(t *testing.T) {
	tests := []struct {
		name string
		ms   int64
		want int64
	}{
		{"zero", 0, 0},
		{"negative is treated as zero", -1000, 0},
		{"one millisecond bills a full minute", 1, 1},
		{"five seconds bills a full minute", 5_000, 1},
		{"exactly one minute", 60_000, 1},
		{"one minute and one millisecond", 60_001, 2},
		{"ninety seconds", 90_000, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CeilMinutes(tt.ms); got != tt.want {
				t.Errorf("CeilMinutes(%d) = %d, want %d", tt.ms, got, tt.want)
			}
		})
	}
}

func TestResolveStandardRunners(t *testing.T) {
	tests := []struct {
		labels []string
		want   float64
	}{
		{[]string{"ubuntu-latest"}, UbuntuUSDPerMinute},
		{[]string{"ubuntu-24.04"}, UbuntuUSDPerMinute},
		{[]string{"macos-latest"}, MacOSUSDPerMinute},
		{[]string{"macos-14"}, MacOSUSDPerMinute},
		{[]string{"windows-latest"}, WindowsUSDPerMinute},
		{[]string{"ubuntu-24.04-arm"}, UbuntuArmUSDPerMinute},
	}
	for _, tt := range tests {
		t.Run(tt.labels[0], func(t *testing.T) {
			r := Resolve(tt.labels)
			if !r.Known {
				t.Fatalf("Resolve(%v) reported unknown", tt.labels)
			}
			if r.USDPerMinute != tt.want {
				t.Errorf("got %v, want %v", r.USDPerMinute, tt.want)
			}
		})
	}
}

// Slim is 3x cheaper than the default. Matching it against the generic ubuntu
// prefix first would silently treble the reported cost of every slim job.
func TestResolveSlimBeforeGenericUbuntu(t *testing.T) {
	r := Resolve([]string{"ubuntu-slim"})
	if !r.Known || r.USDPerMinute != UbuntuSlimUSDPerMinute {
		t.Errorf("ubuntu-slim resolved to %v (known=%v), want %v", r.USDPerMinute, r.Known, UbuntuSlimUSDPerMinute)
	}
}

func TestResolveLargerRunners(t *testing.T) {
	tests := []struct {
		label string
		want  float64
	}{
		{"ubuntu-latest-4-core", 0.012},
		{"ubuntu-latest-16-core", 0.042},
		{"windows-latest-8-core", 0.042},
		{"ubuntu-latest-8-core-arm", 0.014},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			r := Resolve([]string{tt.label})
			if !r.Known {
				t.Fatalf("%s reported unknown", tt.label)
			}
			if r.USDPerMinute != tt.want {
				t.Errorf("got %v, want %v", r.USDPerMinute, tt.want)
			}
		})
	}
}

func TestResolveSelfHostedWinsOverOtherLabels(t *testing.T) {
	r := Resolve([]string{"self-hosted", "linux", "x64"})
	if !r.SelfHosted {
		t.Error("expected SelfHosted")
	}
	if r.USDPerMinute != 0 {
		t.Errorf("self-hosted should carry no hosted rate, got %v", r.USDPerMinute)
	}
	if r.Known {
		t.Error("self-hosted must not be reported as a known billable rate")
	}
}

func TestResolveUnknownLabelIsNotFree(t *testing.T) {
	r := Resolve([]string{"some-custom-pool"})
	if r.Known {
		t.Error("unrecognised label should report Known=false so callers can skip it")
	}
	if r.SelfHosted {
		t.Error("an unrecognised label is not the same as self-hosted")
	}
}

// An unpublished core count must not silently fall back to the 2-core rate.
func TestResolveUnpublishedCoreCountIsUnknown(t *testing.T) {
	if r := Resolve([]string{"ubuntu-latest-48-core"}); r.Known {
		t.Errorf("48-core has no published rate but resolved to %v", r.USDPerMinute)
	}
}

// macOS being an order of magnitude dearer than Linux is the biggest lever in
// the tool. If a rate edit breaks that it is almost certainly a typo.
func TestMacOSIsAnOrderOfMagnitudeDearerThanUbuntu(t *testing.T) {
	if ratio := MacOSUSDPerMinute / UbuntuUSDPerMinute; ratio < 9 || ratio > 12 {
		t.Errorf("macOS/ubuntu ratio is %.1fx, expected ~10x -- check rates against %s", ratio, RatesSource)
	}
}

// A hardcoded price list goes stale silently. The retrieval date is honest but
// passive -- nobody reads a date and does the subtraction -- so the package
// does it, and the report can say so out loud.
func TestRatesStaleness(t *testing.T) {
	retrieved, err := time.Parse("2006-01-02", RatesRetrieved)
	if err != nil {
		t.Fatalf("RatesRetrieved %q is not a date: %v", RatesRetrieved, err)
	}

	if RatesStale(retrieved) {
		t.Error("the table is not stale on the day it was retrieved")
	}
	if RatesStale(retrieved.Add(StaleAfter - time.Hour)) {
		t.Error("still inside the window, should not be stale")
	}
	if !RatesStale(retrieved.Add(StaleAfter + time.Hour)) {
		t.Error("past the window, should be stale")
	}

	if age := RatesAge(retrieved.Add(48 * time.Hour)); age != 48*time.Hour {
		t.Errorf("RatesAge = %v, want 48h", age)
	}
	// A clock behind the retrieval date is a machine with a wrong clock, not a
	// negative age.
	if age := RatesAge(retrieved.Add(-24 * time.Hour)); age != 0 {
		t.Errorf("RatesAge = %v for a clock before the retrieval date, want 0", age)
	}
}
