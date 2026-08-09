# flakewatch

A CLI that measures how flaky a repo's GitHub Actions workflows actually are.

[![CI](https://github.com/karanmonu/flakewatch/actions/workflows/ci.yml/badge.svg)](https://github.com/karanmonu/flakewatch/actions/workflows/ci.yml)

Most teams know their CI is flaky the same way they know the office coffee is bad: anecdotally. Reruns burn compute, mask real failures, and train everyone to ignore red builds. flakewatch puts a number on it.

Point it at any repo and get:

- a flakiness score (0–1) per workflow, based on pass/fail transition rate. An always-failing workflow scores 0 — that's broken, not flaky
- zombie run detection: runs stuck `in_progress` for hours, silently eating runner minutes
- average duration per workflow

## Quickstart

```bash
go install github.com/karanmonu/flakewatch@latest

export GITHUB_TOKEN=ghp_...   # any token with actions:read
flakewatch -repo grafana/k6
```

```
flakewatch report — grafana/k6
------------------------------------------------------------
WORKFLOW                        RUNS  FAIL%   FLAKY  AVG(s)
CI                               142    18%    0.61    412  🔴 flaky
Lint                              89     2%    0.07     88  🟢 stable
```

Machine-readable output for dashboards or CI gates:

```bash
flakewatch -repo owner/name -json | jq '.workflows[0]'
```

## How the flakiness score works

For each workflow's chronological run history:

```
score = transition_rate × 4p(1-p)
```

where `transition_rate` = pass↔fail flips ÷ (runs − 1), and `p` = failure rate. The `4p(1-p)` term peaks at `p = 0.5` and is zero at both extremes — a workflow that alternates outcomes is maximally flaky; one that always passes or always fails is not flaky at all.

## Design notes

No dependencies — stdlib only, so `go install` is instant and there's nothing to audit. Read-only: it needs `actions:read` and never mutates anything. Longer-form reasoning lives in [docs/adr/](docs/adr/).

Current limitations worth knowing: scoring is workflow-level (a flaky job inside a mostly-green workflow gets diluted), and duration is a plain average, so it won't catch a slow regression yet.

## Roadmap

- [ ] job-level flakiness, not just workflow-level
- [ ] duration regression detection (trend, not average)
- [ ] failure-log clustering to group failures by likely root cause
- [ ] GitHub Action mode: comment flakiness deltas on PRs

## License

MIT

