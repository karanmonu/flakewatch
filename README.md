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
flakewatch -repo gohugoio/hugo -runs 200 -since 30d -cost
```

```
flakewatch report — gohugoio/hugo
------------------------------------------------------------------------

Estimated spend: $18.38 over 3.3 days  (window too short to project a month)
This is what the runs would cost at published rates. Public repositories
are not billed for standard GitHub-hosted runners.
Asked for 30 days but hit the run cap first, so this covers 3.3 days.
About -runs 455 would cover the full window, at one request per run.

50 runs priced. 36 of them concluded success or failure and are scored
below; the other 14 were cancelled or skipped, which costs money but says
nothing about flakiness.

WORKFLOW                      RUNS  FAIL%   FLAKY  AVG(s)      COST
Test                            18    11%    0.09    3822    $17.07  🟢 stable
Continuous Integration           7     0%    0.00     146     $0.79  🟢 stable
Update Docs Helpers              2     0%       -     172     $0.31  only 2 run(s)

Spend on platforms dearer than Linux:
WORKFLOW                     PLATFORM  JOBS      COST    ON LINUX  DIFFERENCE
Test                         windows     18    $10.67       $6.40       $4.27
```

`Test` is 93% of that bill, and $10.67 of it is Windows legs that would cost $6.40 on Linux. Neither fact is available anywhere in the GitHub UI.

## What it says about repositories you know

Measured, not illustrative — [`.github/workflows/survey.yml`](.github/workflows/survey.yml) produced this and you can re-run it. Every row is `-runs 50 -since 30d`, so most windows are short; that is the point of the second column.

| Repository | Window measured | Spend in it | Largest single line | Dearer-platform spend |
|---|---:|---:|---|---:|
| `gohugoio/hugo` | 3.3 days | $18.38 | `Test` — 93% of the bill | $10.67 Windows, $6.40 on Linux |
| `grafana/k6` | 2.4 hours | $10.45 | `E2E` | $3.29 macOS, $0.32 on Linux |
| `prometheus/prometheus` | 2.4 hours | $9.79 | `CI` | $1.17 Windows, $0.70 on Linux |
| `rs/zerolog` | 28.2 days | $9.65 | `Test` | $8.56 macOS, $0.83 on Linux |
| `golangci/golangci-lint` | 1.8 days | $8.82 | `Tests` | $4.22 macOS, $0.41 on Linux |
| `cli/cli` | 2.4 hours | $5.21 | `Unit and Integration Tests` | $3.60 macOS, $0.35 on Linux |
| `vitejs/vite` | 9.6 hours | $3.60 | `CI` | — |
| `hashicorp/terraform` | 2.4 hours | $3.22 | `build` | $0.19 macOS, $0.02 on Linux |

Two things fall out of that table.

**Fifty runs is two and a half hours on a busy repository.** k6, prometheus, cli/cli and terraform all burned through the cap inside a morning. The window column is the honest answer to "over what period?", and the tool tells you what `-runs` value would have covered the window you asked for.

**`rs/zerolog` is the only row with a monthly figure**, because it is the only one where 50 runs spanned more than a week. That restraint is deliberate — see [below](#what-it-will-not-do).

## Fix the window, not the sample size

```bash
flakewatch -repo owner/name -since 30d -cost
```

A run count covers however many days that repository happened to be busy for, so the same command run twice gives two different windows and two different monthly figures. A fixed window is comparable between runs. `-runs` still caps what the analysis can spend on API requests, and when the cap is reached first the report says how much of the window it actually covered and what `-runs` would have covered the rest.

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
      - uses: karanmonu/flakewatch@v0.5.0
```

It stays quiet by default: no workflow files changed, no comment. The comment is scoped to the workflows *that* pull request edits, and it edits its own comment rather than adding a new one on every push.

| Input | Default | What it does |
|---|---|---|
| `github-token` | `${{ github.token }}` | Needs `actions:read` and `pull-requests:write` |
| `runs` | `50` | Recent runs to analyze. One API request each |
| `version` | `v0.5.0` | Release to download |
| `always-comment` | `false` | Comment on every PR, not just workflow changes |

Two properties it holds to, because a reporting tool that breaks your pipeline is worse than no reporting tool:

- **It never fails your build.** Every failure path — an unreachable release asset, a bad token, a fork's read-only token — becomes a workflow annotation and a green check.
- **It leaves your API budget alone.** Inside Actions the `GITHUB_TOKEN` allows roughly 1,000 requests an hour, *shared with every other workflow in the repository*. flakewatch reads its remaining budget from the response headers, keeps 100 requests in reserve, and shrinks the sample rather than spending the lot. A truncated analysis says so in the comment.

## Runs that kept going after a newer commit replaced them

Someone pushes to a pull request branch, CI starts, they push again two minutes later. Without a concurrency group the first run carries on to the end, and every minute after that second push buys a result for a commit nobody will look at.

```
Runs that kept going after a newer commit replaced them:
WORKFLOW                      RUNS   MINUTES        COST  PER MONTH
CI                              14        86       $0.52  ~$4.83/mo
```

Counted from the moment the newer run started, not the whole run — the minutes before that were buying a result someone still wanted. Reporting the whole run would be the larger, more impressive number and it would be wrong.

Pull request events only. Two overlapping runs on the default branch are one build per commit, which is how you find out which commit broke something; flagging that would be wrong on nearly every repository that exists.

This is the one table here phrased as a defect rather than an observation, because there is no reading under which a finished run for a deleted commit was working as intended. The fix is four lines:

```yaml
concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true
```

`cancel-in-progress` is the load-bearing line. Without it the group queues the newer run behind the older one instead of replacing it.

## Which part of the workflow

"`Test` is 93% of the bill" is half an answer. The next question is always which part of `Test`, and until now the answer was to go and read the logs.

```
Where the time goes, by step:
WORKFLOW                 STEP                               RAN   MINUTES      SHARE
Test                     Test                               216      1841     $11.05
Test                     Set up job                         216        58      $0.35
Test                     Install dependencies               216        44      $0.26
```

The jobs endpoint already returns every step with its timestamps, in the same response the cost arithmetic was reading anyway, so this costs no extra API requests. Matrix legs count separately — a four-second step fanned out twelve ways is not a four-second step, and `RAN` is what shows you that.

Read the money as a **share, not a charge**. GitHub bills the job, rounded up to the whole minute; a step is a slice of that. The steps sum to slightly less than the workflow totals, and the difference is the rounding.

Step timestamps have no sub-second component, so anything that finishes inside a second reports as zero and is left out.

## Runners the rate table has never heard of

Self-hosted runners, organisation runner groups and larger runners carry labels somebody chose — `gpu-large`, `buildjet-8vcpu-ubuntu-2204` — and no lookup table will ever know what those cost. Those jobs used to be excluded and named, which made the total an undercount for exactly the repositories with the largest bill. Now you can tell it:

```bash
cat > rates.json <<'EOF'
{
  "gpu-large": 0.42,
  "github-hosted-windows-x64-large": 0.064
}
EOF

flakewatch -repo owner/name -cost -rates rates.json
```

Values are USD per minute. They win over both the published table and the self-hosted rule, so a machine you own gets priced at what it costs you, and a negotiated enterprise rate beats list price. Where several of a job's labels appear in the file, the longest one wins as the more specific description of the machine.

When labels are still unpriced the report prints the file you would need with the labels already filled in, rather than only naming the gap.

## How the cost is measured

Costs come from the **jobs** endpoint, not the run timing endpoint. Two reasons, both found by calling the API rather than reading the docs:

- timing reports zero billable time for public repositories, which GitHub does not bill. `grafana/k6` returns a literally empty `billable` object, so anything built on it silently reports $0 for every public repo.
- timing reports only UBUNTU/MACOS/WINDOWS, so a 32-core runner is indistinguishable from a 2-core one. Job records carry the actual runner label.

Billing rounds **each job** up to the whole minute, not the run. A run of ten 30-second jobs bills ten minutes, not five, and the gap widens the wider a matrix fans out — which is exactly where the money is.

## How the flakiness score works

```
score = transition_rate × 4p(1-p)
```

`transition_rate` is pass/fail flips divided by (scored runs - 1); `p` is the failure rate. The `4p(1-p)` term peaks at `p = 0.5` and is zero at both extremes: a workflow that alternates outcomes is maximally flaky, one that always passes or always fails is not flaky at all.

`RUNS` and `SCORED` are different numbers on purpose. Everything that ran costs money; only the runs that concluded success or failure say anything about flakiness. Cancelled runs belong in one column and not the other.

Workflows with fewer than five scored runs get no score. Two runs that differ is one coin flip, and reporting that as maximal flakiness was a real bug.

## What it will not do

Jobs whose runner label has no published rate and no entry in your `-rates` file are excluded and **named in the output** rather than silently counted as free. A visible gap beats a confident wrong number.

The platform table is an observation, not a recommendation. flakewatch can see that a workflow spends on macOS; it cannot see whether that workflow needs macOS.

**No monthly projection from less than a week of measured history.** CI load is weekly-periodic — weekdays are busy, weekends are nearly dead — so a shorter window misstates the month by whenever you happened to run it. Scaling golangci-lint's 1.8 consecutive weekdays to 30 days produced "$143/month", which was an artefact of the sampling time rather than a measurement. Most rows in the table above get no monthly figure as a result. A missing number invites a second look; a wrong one does not.

Scoring is workflow-level, so a flaky job inside a mostly-green workflow gets diluted.

## Design notes

No dependencies — stdlib only, so `go install` is instant and there is nothing to audit. Read-only: needs `actions:read`, never mutates anything. Reasoning in [docs/adr/](docs/adr/).

A workflow is identified by its **file**, not its display name. Two files can both be `name: CI`, and a workflow can be renamed mid-window; keying on the name merges the first pair into one row and splits the second into two.

CI runs flakewatch against this repository on every build, so a change that breaks against the real API fails the build rather than shipping.

## Roadmap

- [x] cost attribution per workflow
- [x] macOS and Windows spend against the Linux equivalent
- [x] GitHub Action mode: comment cost and flakiness on PRs
- [x] time-window sampling (`-since 30d`) instead of a fixed run count
- [x] user-supplied rates for unrecognised and self-hosted runner labels
- [x] runs that kept going after a newer commit replaced them
- [x] per-step cost attribution
- [ ] job-level flakiness, not just workflow-level
- [ ] duration regression detection (trend, not average)

## License

MIT
