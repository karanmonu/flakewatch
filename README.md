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

