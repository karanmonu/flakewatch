// Command flakewatch analyzes GitHub Actions workflow history for a repository
// and reports flaky workflows, zombie (stuck) runs, and duration trends.
//
// Usage:
//
//	flakewatch -repo owner/name [-runs 200] [-zombie-hours 6]
//
// Authentication uses the GITHUB_TOKEN environment variable (a classic PAT or
// fine-grained token with actions:read is sufficient).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/karanmonu/flakewatch/internal/analyze"
	"github.com/karanmonu/flakewatch/internal/gh"
	"github.com/karanmonu/flakewatch/internal/report"
)

func main() {
	repo := flag.String("repo", "", "repository in owner/name form (required)")
	runs := flag.Int("runs", 200, "number of recent workflow runs to analyze")
	zombieHours := flag.Float64("zombie-hours", 6, "runs in progress longer than this are flagged as zombies")
	jsonOut := flag.Bool("json", false, "emit JSON instead of a terminal report")
	flag.Parse()

	if *repo == "" {
		fmt.Fprintln(os.Stderr, "error: -repo is required (e.g. -repo grafana/k6)")
		flag.Usage()
		os.Exit(2)
	}

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "warning: GITHUB_TOKEN not set; using unauthenticated requests (60 req/hr limit)")
	}

	client := gh.NewClient(token)
	workflowRuns, err := client.ListWorkflowRuns(*repo, *runs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error fetching workflow runs: %v\n", err)
		os.Exit(1)
	}

	result := analyze.Analyze(workflowRuns, analyze.Options{ZombieHours: *zombieHours})

	if *jsonOut {
		if err := report.WriteJSON(os.Stdout, result); err != nil {
			fmt.Fprintf(os.Stderr, "error writing JSON: %v\n", err)
			os.Exit(1)
		}
		return
	}
	report.WriteTerminal(os.Stdout, *repo, result)
}

