package analyze

import (
	"sort"

	"github.com/karanmonu/flakewatch/internal/gh"
	"github.com/karanmonu/flakewatch/internal/pricing"
)

// Opportunity is a costed observation about where a workflow's money goes.
//
// It is deliberately not phrased as a recommendation. flakewatch can see that a
// workflow spends on macOS; it cannot see whether that workflow needs macOS.
// Presenting "you could save $X" as a finding would be wrong roughly as often
// as it was right -- plenty of macOS spend is buying something real. So this
// reports the number and leaves the judgement to someone who knows the repo.
type Opportunity struct {
	Workflow string `json:"workflow"`
	// Platform is the dearer platform the spend is on.
	Platform string `json:"platform"`
	// Jobs is how many jobs contributed.
	Jobs int `json:"jobs"`
	// CurrentUSD is what those jobs cost over the window.
	CurrentUSD float64 `json:"current_usd"`
	// OnLinuxUSD is what the same minutes would have cost on a standard Linux
	// runner.
	OnLinuxUSD float64 `json:"on_linux_usd"`
	// DeltaUSD is the difference over the window.
	DeltaUSD float64 `json:"delta_usd"`
	// MonthlyDeltaUSD extrapolates the difference to 30 days. Zero when the
	// window was too short to extrapolate.
	MonthlyDeltaUSD float64 `json:"monthly_delta_usd"`
}

type platformKey struct {
	workflow string
	platform pricing.Platform
}

type platformSpend struct {
	minutes int64
	usd     float64
	jobs    int
}

// findOpportunities totals non-Linux spend per workflow and prices the Linux
// counterfactual.
//
// monthlyFactor scales the window to 30 days, or is zero when the window was
// too short to extrapolate honestly.
func findOpportunities(runs []gh.WorkflowRun, jobs gh.JobsResult, monthlyFactor float64, rates pricing.Overrides) []Opportunity {
	spend := make(map[platformKey]*platformSpend)
	// Same identity as everywhere else: the file, not the display name. Two
	// files called "CI" are two workflows, and their macOS spend should not be
	// added together under one row.
	displayName := make(map[string]string)

	for _, r := range runs {
		displayName[workflowKey(r)] = r.Name
		for _, j := range jobs.ByRun[r.ID] {
			if j.DurationMS() == 0 {
				continue
			}
			runner := pricing.ResolveWith(j.Labels, rates)
			if !runner.Known {
				continue
			}
			// A user-priced runner is deliberately left out of this table. The
			// table's whole claim is "the same minutes on a standard Linux
			// runner would cost this instead", and that comparison is only
			// meaningful between two GitHub-hosted SKUs. A self-hosted GPU box
			// priced from someone's cloud bill is not a machine you swap for
			// ubuntu-latest, and pricing the swap would invent a saving that
			// does not exist.
			if runner.UserSupplied {
				continue
			}
			platform := pricing.PlatformOf(runner.Label)
			if platform == pricing.Linux || platform == pricing.UnknownPlatform {
				continue
			}

			minutes := pricing.CeilMinutes(j.DurationMS())
			key := platformKey{workflow: workflowKey(r), platform: platform}
			s := spend[key]
			if s == nil {
				s = &platformSpend{}
				spend[key] = s
			}
			s.minutes += minutes
			s.usd += float64(minutes) * runner.USDPerMinute
			s.jobs++
		}
	}

	out := make([]Opportunity, 0, len(spend))
	for key, s := range spend {
		onLinux := float64(s.minutes) * pricing.LinuxEquivalentUSDPerMinute
		o := Opportunity{
			Workflow:   displayName[key.workflow],
			Platform:   string(key.platform),
			Jobs:       s.jobs,
			CurrentUSD: s.usd,
			OnLinuxUSD: onLinux,
			DeltaUSD:   s.usd - onLinux,
		}
		o.MonthlyDeltaUSD = o.DeltaUSD * monthlyFactor
		out = append(out, o)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].DeltaUSD != out[j].DeltaUSD {
			return out[i].DeltaUSD > out[j].DeltaUSD
		}
		return out[i].Workflow < out[j].Workflow // stable for equal spend
	})
	return out
}
