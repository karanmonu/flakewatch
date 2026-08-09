// Package report renders analysis results for terminals and machines.
package report

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/karanmonu/flakewatch/internal/analyze"
)

// WriteJSON emits the result as indented JSON.
func WriteJSON(w io.Writer, r analyze.Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteTerminal renders a human-readable report.
func WriteTerminal(w io.Writer, repo string, r analyze.Result) {
	fmt.Fprintf(w, "\nflakewatch report — %s\n", repo)
	fmt.Fprintf(w, "%s\n\n", line(60))

	if len(r.Workflows) == 0 {
		fmt.Fprintln(w, "No completed workflow runs found.")
	} else {
		fmt.Fprintf(w, "%-30s %5s %6s %7s %7s\n", "WORKFLOW", "RUNS", "FAIL%", "FLAKY", "AVG(s)")
		for _, s := range r.Workflows {
			fmt.Fprintf(w, "%-30s %5d %5.0f%% %7.2f %7.0f  %s\n",
				truncate(s.Name, 30), s.Runs, s.FailureRate*100, s.FlakinessScore, s.AvgDurationSec, badge(s.FlakinessScore))
		}
	}

	if len(r.Zombies) > 0 {
		fmt.Fprintf(w, "\nZombie runs (stuck in progress):\n")
		for _, z := range r.Zombies {
			fmt.Fprintf(w, "  %-30s %.1fh  %s\n", truncate(z.Workflow, 30), z.Hours, z.URL)
		}
	}
	fmt.Fprintln(w)
}

func badge(score float64) string {
	switch {
	case score >= 0.5:
		return "🔴 flaky"
	case score >= 0.2:
		return "🟡 unstable"
	default:
		return "🟢 stable"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func line(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '-'
	}
	return string(b)
}

