package pricing

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
)

// Overrides maps a runner label to a rate in USD per minute, supplied by the
// user rather than read from GitHub's published table.
//
// This exists because the published table cannot cover the cases where the
// money actually is. Self-hosted runners are not billed by GitHub but are not
// free -- somebody pays for the machine. Larger runners and organisation
// runner groups carry labels chosen by whoever created them ("gpu-large",
// "buildjet-8vcpu-ubuntu-2204"), and no lookup table will ever know what those
// cost. Without this, flakewatch excludes exactly those jobs, which means it
// undercounts hardest for the people with the largest bill.
//
// Keys are compared against the job's labels after lowercasing and trimming,
// so "Self-Hosted-GPU" in the file matches "self-hosted-gpu" on the job.
type Overrides map[string]float64

// maxOverrideUSDPerMinute is a sanity ceiling on a user-supplied rate.
//
// The most expensive published GitHub SKU is $0.552/min. Ten dollars a minute
// is $14,400 a day for one runner; anyone typing that has misread the units --
// almost always by entering an hourly rate, or a monthly instance price, in a
// field that wants per-minute. Guessing which and silently dividing would be
// worse than refusing, so this refuses and says what the units are.
const maxOverrideUSDPerMinute = 10.0

// LoadOverrides reads a rate file.
//
// The format is deliberately the smallest thing that works -- a JSON object of
// label to USD-per-minute:
//
//	{
//	  "self-hosted-gpu":                    0.42,
//	  "github-hosted-windows-x64-large":    0.064,
//	  "buildjet-8vcpu-ubuntu-2204":         0.016
//	}
//
// No schema version, no nesting, nothing to learn. The file is something a
// person writes by hand after reading their own cloud bill, and every field it
// could have gained is a field they could get wrong.
func LoadOverrides(path string) (Overrides, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading rates file: %w", err)
	}

	// Decoding straight into map[string]float64 does the shape check for free:
	// a nested object, a quoted number, or a list all fail here rather than
	// loading as an empty map, which would look exactly like the flag being
	// ignored and send the reader hunting in the wrong place.
	var parsed map[string]float64
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parsing %s: %w (expected a JSON object of runner label to USD per minute)", path, err)
	}
	if len(parsed) == 0 {
		return nil, fmt.Errorf("%s contains no rates", path)
	}

	ov := make(Overrides, len(parsed))
	for label, rate := range parsed {
		key := strings.ToLower(strings.TrimSpace(label))
		if key == "" {
			return nil, fmt.Errorf("%s: empty runner label", path)
		}
		switch {
		case math.IsNaN(rate), math.IsInf(rate, 0):
			return nil, fmt.Errorf("%s: rate for %q is not a number", path, label)
		case rate < 0:
			return nil, fmt.Errorf("%s: rate for %q is negative", path, label)
		case rate > maxOverrideUSDPerMinute:
			return nil, fmt.Errorf("%s: rate for %q is %g USD per minute, which is almost certainly an hourly or monthly figure -- rates are per minute (GitHub's dearest published runner is %g)",
				path, label, rate, 0.552)
		}
		ov[key] = rate
	}
	return ov, nil
}

// lookup finds the override that applies to a set of job labels.
//
// A job can carry several labels ("self-hosted", "linux", "gpu-large") and more
// than one may appear in the file. The longest matching label wins, on the
// principle that a longer label is the more specific description of the
// machine: "gpu-large" says more about what the job ran on than "linux" does.
// Ties -- two matches of equal length -- go to the lexically first, so the
// result never depends on map iteration order.
func (o Overrides) lookup(normalised []string) (string, float64, bool) {
	var (
		bestLabel string
		bestRate  float64
		found     bool
	)
	for _, l := range normalised {
		rate, ok := o[l]
		if !ok {
			continue
		}
		if !found || len(l) > len(bestLabel) || (len(l) == len(bestLabel) && l < bestLabel) {
			bestLabel, bestRate, found = l, rate, true
		}
	}
	return bestLabel, bestRate, found
}
