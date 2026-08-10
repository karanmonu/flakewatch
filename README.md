# flakewatch

Find which GitHub Actions workflow is costing you money, and which one is flaky.

[![CI](https://github.com/karanmonu/flakewatch/actions/workflows/ci.yml/badge.svg)](https://github.com/karanmonu/flakewatch/actions/workflows/ci.yml)

GitHub bills Actions in aggregate, so when the bill moves there is no way to find out which workflow moved it. GitHub's own documented answer is to export a usage CSV and process it offline. flakewatch answers it from your repository's run history instead.

## Install

Download a binary from [releases](https://github.com/karanmonu/flakewatch/releases), or:

```bash
go install github.com/karanmonu/flakewatch@latest
```

## Use

```bash
export GITHUB_TOKEN=...   # any token with actions:read
flakewatch -repo grafana/k6 -runs 200 -cost
```

Real output:

```
flakewatch report — grafana/k6
------------------------------------------------------------------------

Estimated spend: $24.12 over 3.2 days  (~$224/month at this rate)
This is what the runs would cost at published rates. Public repositories
are not billed for standard GitHub-hosted runners.
20 job(s) used a runner label with no published rate and are excluded,
so this is an undercount. Labels: github-hosted-windows-x64-large

WORKFLOW                      RUNS  FAIL%   FLAKY  AVG(s)      COST
Test                            10    50%    0.33     741     $4.38  🟡 unstable
Lint                            10    40%    0.32     118     $0.25  🟡 unstable
Browser tests                   10    10%    0.08    1096     $3.15  🟢 stable
xk6                             10    10%    0.08     215     $4.85  🟢 stable
E2E                             12     8%    0.06     313     $9.68  🟢 stable
TC39                             3     0%       -      43     $0.02  only 3 run(s)

Spend on platforms dearer than Linux:
WORKFLOW                     PLATFORM  JOBS      COST    ON LINUX  DIFFERENCE
E2E                          macos       24     $7.94       $0.77  $7.17 (~$66.67/mo)
xk6                          macos       20     $3.53       $0.34  $3.19 (~$29.69/mo)
Test                         windows     20     $2.47       $1.48  $0.99 (~$9.19/mo)

These are not recommendations. Jobs that genuinely need macOS or Windows
should stay there -- this only shows what that choice costs.
```

Analyze a fixed time window rather than a fixed number of runs:

```bash
flakewatch -repo owner/name -since 30d -cost
```

This is the difference between a monthly figure that moves every time you run it and one that does not — a run count covers however many days that repo happened to be busy for. `-runs` still caps how many API requests the window can cost, and the report says so when the cap is reached before the window is covered.

Machine-readable, for dashboards or CI gates:

```bash
flakewatch -repo owner/name -cost -json | jq '.cost'
```

## Use it as a GitHub Action

Comment the same numbers on any pull request that touches a workflow file:

```yaml
name: flakewatch
on: pull_request

permissions:
  contents: read
  actions: read
  pull-requests: write

jobs:
  report:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0   # needed to see which files the PR changed
      - uses: karanmonu/flakewatch@v0.3.0
```

It stays quiet by default: no workflow files changed, no comment. When it does comment, it edits its own comment rather than adding a new one on every push.

The comment leads with the workflows **this** pull request edits, and shows the rest of CI underneath — "is this change expensive" is only answerable against what everything else costs. Matching is on the workflow file path rather than its name, because names collide and get renamed while `.github/workflows/ci.yml` is what a diff actually gives you.

| Input | Default | What it does |
|---|---|---|
| `github-token` | `${{ github.token }}` | Needs `actions:read` and `pull-requests:write` |
| `runs` | `50` | Cap on runs analyzed. One API request each |
| `since` | `30d` | Time window to analyze. Keeps the monthly figure stable |
| `version` | `v0.3.0` | Release to download |
| `always-comment` | `false` | Comment on every PR, not just workflow changes |

Two properties it holds to, because a reporting tool that breaks your pipeline is worse than no reporting tool:

- **It never fails your build.** Every failure path — an unreachable release asset, a bad token, a fork's read-only token — becomes a workflow annotation and a green check.
- **It leaves your API budget alone.** Inside Actions the `GITHUB_TOKEN` allows roughly 1,000 requests an hour, *shared with every other workflow in the repository*. flakewatch reads its remaining budget from the response headers, keeps 100 requests in reserve, and shrinks the sample rather than spending the lot. A truncated analysis says so in the comment.

## How the cost is measured

Costs come from the **jobs** endpoint, not the run timing endpoint. Two reasons, both found by calling the API rather than reading the docs:

- timing reports zero billable time for public repositories, which GitHub does not bill. `grafana/k6` returns a literally empty `billable` object.
- timing reports only UBUNTU/MACOS/WINDOWS, so a 32-core runner is indistinguishable from a 2-core one. Job records carry the actual runner label.

Billing rounds **each job** up to the whole minute, not the run. A run of ten 30-second jobs bills ten minutes, not five, and the gap widens the wider a matrix fans out — which is exactly where the money is.

## How the flakiness score works

```
score = transition_rate × 4p(1-p)
```

`transition_rate` is pass/fail flips divided by (runs - 1); `p` is the failure rate. The `4p(1-p)` term peaks at `p = 0.5` and is zero at both extremes: a workflow that alternates outcomes is maximally flaky, one that always passes or always fails is not flaky at all.

Workflows with fewer than five runs in the window get no score. Two runs that differ is one coin flip, and reporting that as maximal flakiness was a real bug.

## What it will not do

Jobs on self-hosted runners, and jobs whose runner label has no published rate, are excluded and **named in the output** rather than silently counted as free. A visible gap beats a confident wrong number.

The platform table is an observation, not a recommendation. flakewatch can see that a workflow spends on macOS; it cannot see whether that workflow needs macOS.

The **monthly projection is the weakest number here**. With a plain `-runs` count it scales whatever window those runs happened to cover, and on a busy repository two samples days apart gave $104/mo and $224/mo ([#11](https://github.com/karanmonu/flakewatch/issues/11)). `-since 30d` fixes the window instead of the sample size, which is what makes the number comparable between runs; if the run cap is hit first, the report says how much of the window it actually covered. Per-window and per-platform figures are measured and stable either way.

Scoring is workflow-level, so a flaky job inside a mostly-green workflow gets diluted.

## Design notes

No dependencies — stdlib only, so `go install` is instant and there is nothing to audit. Read-only: needs `actions:read`, never mutates anything. Reasoning in [docs/adr/](docs/adr/).

CI runs flakewatch against this repository on every build, so a change that breaks against the real API fails the build rather than shipping.

## Roadmap

- [x] cost attribution per workflow
- [x] macOS and Windows spend against the Linux equivalent
- [ ] job-level flakiness, not just workflow-level
- [ ] missing concurrency groups and uncached dependency installs
- [ ] user-supplied rates for unrecognised and self-hosted runner labels
- [x] GitHub Action mode: comment cost and flakiness on PRs
- [x] time-window sampling (`-since 30d`) instead of a fixed run count

## License

MIT
