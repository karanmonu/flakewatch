// Package pricing maps GitHub Actions runner labels to published per-minute
// rates, and holds the billing arithmetic that goes with them.
//
// Rates are hardcoded data that will drift. They live in this one file with
// their source and retrieval date, so there is one place to update and no
// ambiguity about how current they are.
package pricing

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RatesSource is where the rate tables below came from.
const RatesSource = "https://docs.github.com/en/billing/reference/actions-runner-pricing"

// RatesRetrieved is the date the tables were last checked against the source.
const RatesRetrieved = "2026-08-10"

// StaleAfter is how long a hardcoded price list gets the benefit of the doubt.
//
// Six months is a guess, but an unguarded guess is worse: the failure mode of
// a stale table is a confident number that is quietly wrong, which is the one
// failure mode this tool exists to avoid. Saying "this data is nine months old"
// costs a line and lets the reader decide.
const StaleAfter = 180 * 24 * time.Hour

// ratesRetrievedAt is RatesRetrieved parsed once. A malformed constant would be
// a build-time mistake, so a zero time here means "treat it as unknown age"
// rather than panicking in front of a user.
var ratesRetrievedAt, _ = time.Parse("2006-01-02", RatesRetrieved)

// RatesAge is how long ago the table was checked, or zero if that is unknown.
func RatesAge(now time.Time) time.Duration {
	if ratesRetrievedAt.IsZero() || now.Before(ratesRetrievedAt) {
		return 0
	}
	return now.Sub(ratesRetrievedAt)
}

// RatesStale reports whether the table is old enough to warn about.
func RatesStale(now time.Time) bool {
	return RatesAge(now) > StaleAfter
}

// Standard GitHub-hosted runner rates, USD per minute.
const (
	UbuntuUSDPerMinute     = 0.006
	UbuntuArmUSDPerMinute  = 0.005
	UbuntuSlimUSDPerMinute = 0.002
	MacOSUSDPerMinute      = 0.062
	WindowsUSDPerMinute    = 0.010
)

// Larger-runner rates by core count.
var (
	linuxByCore = map[int]float64{
		2: 0.006, 4: 0.012, 8: 0.022, 16: 0.042, 32: 0.082, 64: 0.162, 96: 0.252,
	}
	linuxArmByCore = map[int]float64{
		2: 0.005, 4: 0.008, 8: 0.014, 16: 0.026, 32: 0.050, 64: 0.098,
	}
	windowsByCore = map[int]float64{
		4: 0.022, 8: 0.042, 16: 0.082, 32: 0.162, 64: 0.322, 96: 0.552,
	}
	windowsArmByCore = map[int]float64{
		2: 0.008, 4: 0.014, 8: 0.026, 16: 0.050, 32: 0.098, 64: 0.194,
	}
)

var coreCountRe = regexp.MustCompile(`(\d+)-?core`)

// Runner is what we could work out about the machine a job ran on.
type Runner struct {
	// Label is the runner label the job requested, e.g. "ubuntu-latest".
	Label string
	// USDPerMinute is the published rate. Zero when Known is false.
	USDPerMinute float64
	// Known reports whether we recognised the label. Callers must not treat an
	// unknown runner as free: "we have no rate" is not "costs nothing".
	Known bool
	// SelfHosted reports a self-hosted runner. GitHub does not currently bill
	// for these, so zero here is a real zero rather than a gap.
	SelfHosted bool
	// UserSupplied reports that USDPerMinute came from the user's rate file
	// rather than GitHub's published table. The report distinguishes the two,
	// because "GitHub charges this" and "you told me this" carry different
	// weight and blurring them would launder a guess into a published figure.
	UserSupplied bool
}

// Resolve works out the rate for a job's runner labels from published rates.
func Resolve(labels []string) Runner { return ResolveWith(labels, nil) }

// ResolveWith works out the rate for a job's runner labels, consulting a
// user-supplied rate table first.
//
// A job carries every label it requested, so "ubuntu-latest" arrives as
// ["ubuntu-latest"] and a self-hosted ARM box might arrive as
// ["self-hosted", "linux", "arm64"].
//
// Order of precedence, and why:
//
//  1. The user's rate file. It beats both the published table and the
//     self-hosted rule, because both of those are statements about what GitHub
//     invoices and the rate file is a statement about what the machine costs.
//     Someone who writes down what their GPU box costs an hour has better
//     information than this program does, and overriding "ubuntu-latest" is a
//     legitimate thing to want: an enterprise agreement is not list price.
//  2. Self-hosted. Not billed by GitHub, so no hosted rate applies.
//  3. The published table.
func ResolveWith(labels []string, ov Overrides) Runner {
	normalised := make([]string, 0, len(labels))
	for _, l := range labels {
		normalised = append(normalised, strings.ToLower(strings.TrimSpace(l)))
	}

	if label, rate, ok := ov.lookup(normalised); ok {
		return Runner{Label: label, USDPerMinute: rate, Known: true, UserSupplied: true}
	}

	for _, l := range normalised {
		if l == "self-hosted" {
			return Runner{Label: strings.Join(labels, ","), SelfHosted: true}
		}
	}

	for _, l := range normalised {
		if r, ok := resolveOne(l); ok {
			return Runner{Label: l, USDPerMinute: r, Known: true}
		}
	}
	return Runner{Label: strings.Join(labels, ",")}
}

func resolveOne(label string) (float64, bool) {
	arm := strings.Contains(label, "arm")

	var cores int
	if m := coreCountRe.FindStringSubmatch(label); m != nil {
		cores, _ = strconv.Atoi(m[1])
	}

	switch {
	case strings.HasPrefix(label, "ubuntu"), strings.HasPrefix(label, "linux"):
		// Slim is a distinct, much cheaper SKU and must be checked before the
		// generic ubuntu rate, otherwise it silently bills at 3x.
		if strings.Contains(label, "slim") {
			return UbuntuSlimUSDPerMinute, true
		}
		if cores > 0 {
			table := linuxByCore
			if arm {
				table = linuxArmByCore
			}
			if r, ok := table[cores]; ok {
				return r, true
			}
			// A core count we have no published rate for. Say so rather than
			// falling back to the 2-core rate and understating the bill.
			return 0, false
		}
		if arm {
			return UbuntuArmUSDPerMinute, true
		}
		return UbuntuUSDPerMinute, true

	case strings.HasPrefix(label, "windows"):
		if cores > 0 {
			table := windowsByCore
			if arm {
				table = windowsArmByCore
			}
			if r, ok := table[cores]; ok {
				return r, true
			}
			return 0, false
		}
		return WindowsUSDPerMinute, true

	case strings.HasPrefix(label, "macos"):
		return MacOSUSDPerMinute, true
	}
	return 0, false
}

// CeilMinutes converts a duration in milliseconds to billable minutes.
//
// GitHub rounds each job up to the next whole minute. A 5-second job and a
// 59-second job both bill one minute.
func CeilMinutes(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	const msPerMinute = 60_000
	return (ms + msPerMinute - 1) / msPerMinute
}
