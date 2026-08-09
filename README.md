# flakewatch

Measures how flaky your GitHub Actions workflows are, and what they cost.

[![CI](https://github.com/karanmonu/flakewatch/actions/workflows/ci.yml/badge.svg)](https://github.com/karanmonu/flakewatch/actions/workflows/ci.yml)

Most teams know their CI is flaky the same way they know the office coffee is bad: anecdotally. And GitHub bills Actions in aggregate, so when the number goes up there is no way to find out which workflow did it. flakewatch puts numbers on both.

Point it at any repo and get:

- a flakiness score (0-1) per workflow, from the pass/fail transition rate. An always-failing workflow scores 0 -- that is broken, not flaky
- an estimate of what each workflow costs, priced per job from the published runner rates
- zombie run detection: runs stuck `in_progress` for hours, quietly eating runner minutes

## Quickstart

```bash
go install github.com/karanmonu/flakewatch@latest

export GITHUB_TOKEN=...   # any token with actions:read
flakewatch -repo owner/name -cost
```

Real output, produced by [this repo's own CI](https://github.com/karanmonu/flakewatch/actions/workflows/ci.yml) on every build:

```
flakewatch report — karanmonu/flakewatch
------------------------------------------------------------------------

Estimated spend: $0.10 over 0.1 days  (window too short to project a month)
This is what the runs would cost at published rates. Public repositories
are not billed for standard GitHub-hosted runners.

WORKFLOW                      RUNS  FAIL%   FLAKY  AVG(s)      COST
CI                              15    20%    0.27      40     $0.10  🟡 unstable

Rates: https://docs.github.com/en/billing/reference/actions-runner-pricing (retrieved 2026-08-10)
```

Machine-readable output for dashboards or CI gates:

```bash
flakewatch -repo owner/name -cost -json | jq '.cost'
```

## How the flakiness score works

For each workflow's chronological run history:

```
score = transition_rate × 4p(1-p)
```

where `transition_rate` is pass/fail flips divided by (runs - 1), and `p` is the failure rate. The `4p(1-p)` term peaks at `p = 0.5` and is zero at both extremes: a workflow that alternates outcomes is maximally flaky, one that always passes or always fails is not flaky at all.

## How the cost estimate works

Costs come from the jobs endpoint, not the run timing endpoint. Two reasons, both found by calling the API rather than reading the docs:

- **timing reports zero for public repositories.** GitHub does not bill them, so `billable` comes back empty. Job durations are wall-clock and always present.
- **timing does not say which runner.** It reports UBUNTU/MACOS/WINDOWS, so a 32-core job looks identical to a 2-core one. Job labels give the actual SKU, which matters when macOS is 10x Linux and a 16-core box is 7x a standard one.

Billing rounds **each job** up to the whole minute, not the run. A run of ten 30-second jobs bills ten minutes, not five -- and the gap widens the wider a matrix fans out, which is exactly where the money is.

Jobs on self-hosted runners, and jobs whose label has no published rate, are excluded rather than counted as free. When that happens the report says so, because a silent zero is worse than a visible gap.

## Design notes

No dependencies -- stdlib only, so `go install` is instant and there is nothing to audit. Read-only: it needs `actions:read` and never mutates anything. Longer reasoning lives in [docs/adr/](docs/adr/).

CI runs flakewatch against this repository on every build, so a change that breaks against the real API fails the build rather than shipping.

Known limitations: scoring is workflow-level, so a flaky job inside a mostly-green workflow gets diluted. Duration is a plain average and will not catch a slow regression. Both are tracked in the issues.

## Roadmap

- [x] cost attribution per workflow
- [ ] job-level flakiness, not just workflow-level
- [ ] waste rules: macOS legs with no Apple-specific steps, missing concurrency groups, uncached dependency installs
- [ ] duration regression detection
- [ ] GitHub Action mode: comment cost and flakiness deltas on PRs

## License

MIT
