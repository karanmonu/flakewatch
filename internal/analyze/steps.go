package analyze

import (
	"sort"

	"github.com/karanmonu/flakewatch/internal/gh"
	"github.com/karanmonu/flakewatch/internal/pricing"
)

// StepCost is where a workflow's time goes, one level below the workflow.
//
// "Your Test workflow is 93% of the bill" is where flakewatch stopped, and it
// is only half an answer: the next question is always which part of Test, and
// until now the answer was to go and read the logs. The jobs endpoint already
// returns every step with its timestamps, in the same response the cost
// arithmetic was reading anyway, so this costs no extra requests.
//
// Read the money here as a share, not a charge. GitHub bills the *job*, rounded
// up to the whole minute; a step is a slice of that. So these figures are the
// job's rate applied pro rata to the step's wall clock, and they sum to
// slightly less than the job total, which is where the rounding went.
type StepCost struct {
	Workflow string `json:"workflow"`
	Step     string `json:"step"`
	// Executions is how many job executions ran this step. A step inside a
	// twelve-leg matrix counts twelve times per run, which is the point: a
	// four-second step fanned out wide is not a four-second step.
	Executions int `json:"executions"`
	// Seconds is the total wall clock across those executions.
	Seconds float64 `json:"seconds"`
	// USD is the pro-rata share of the jobs' cost.
	USD float64 `json:"usd"`
	// MonthlyUSD extrapolates USD to 30 days, zero when the window was too
	// short to extrapolate honestly.
	MonthlyUSD float64 `json:"monthly_usd,omitempty"`
}

// maxStepCosts caps how many steps are reported.
//
// Ten, because the value of this table is "here are the few things worth
// looking at". A complete listing of every step in every workflow is a log
// file, and people already have one of those.
const maxStepCosts = 10

type stepKey struct {
	workflow string
	step     string
}

type stepTally struct {
	executions int
	seconds    float64
	usd        float64
}

// findStepCosts attributes each workflow's spend across its step names.
//
// Steps are aggregated by name across every job in the workflow, matrix legs
// included. That merges "Build" on Linux with "Build" on Windows, which is
// deliberate: the question this answers is "which part of this workflow is
// expensive", and splitting one step into fourteen matrix rows buries the
// answer under its own detail. The per-platform split already has its own
// table.
//
// Setup and teardown steps ("Set up job", "Post ...") are kept rather than
// filtered. They are real billed time, they are often a surprising share of a
// short job, and a tool that hides the boring rows decides for the reader which
// findings are allowed to be interesting.
func findStepCosts(runs []gh.WorkflowRun, jobs gh.JobsResult, monthlyFactor float64, rates pricing.Overrides) []StepCost {
	tally := make(map[stepKey]*stepTally)
	displayName := make(map[string]string)

	for _, r := range runs {
		displayName[workflowKey(r)] = r.Name
		for _, j := range jobs.ByRun[r.ID] {
			if j.DurationMS() == 0 || len(j.Steps) == 0 {
				continue
			}
			runner := pricing.ResolveWith(j.Labels, rates)

			for _, s := range j.Steps {
				key := stepKey{workflow: workflowKey(r), step: s.Name}
				t := tally[key]
				if t == nil {
					t = &stepTally{}
					tally[key] = t
				}

				// Counted before the duration check, deliberately. Step
				// timestamps have no sub-second component, so anything that
				// finishes inside a second measures as zero -- and an earlier
				// version skipped those entirely, which made this a count of
				// executions lasting at least a second while the column was
				// labelled as a count of executions. Against gohugoio/hugo that
				// showed "Install Go" running 154 times and its own post step
				// 86, an impossible pair that was really 68 sub-second
				// teardowns being dropped. Undercounting quietly is the exact
				// failure this tool exists to avoid.
				t.executions++

				d := s.Duration()
				if d == 0 {
					continue
				}
				t.seconds += d.Seconds()
				if runner.Known {
					// Pro rata, not rounded up: rounding every step to a whole
					// minute would invent minutes the job was never billed for
					// and make the steps sum to several times the job's cost.
					t.usd += d.Minutes() * runner.USDPerMinute
				}
			}
		}
	}

	out := make([]StepCost, 0, len(tally))
	for key, t := range tally {
		out = append(out, StepCost{
			Workflow:   displayName[key.workflow],
			Step:       key.step,
			Executions: t.executions,
			Seconds:    t.seconds,
			USD:        t.usd,
			MonthlyUSD: t.usd * monthlyFactor,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].USD != out[j].USD {
			return out[i].USD > out[j].USD
		}
		// Unpriced runners give every step a cost of zero, and seconds are then
		// the only thing left that orders them usefully.
		if out[i].Seconds != out[j].Seconds {
			return out[i].Seconds > out[j].Seconds
		}
		if out[i].Workflow != out[j].Workflow {
			return out[i].Workflow < out[j].Workflow
		}
		return out[i].Step < out[j].Step
	})

	if len(out) > maxStepCosts {
		out = out[:maxStepCosts]
	}
	return out
}
