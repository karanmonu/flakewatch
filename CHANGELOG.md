# Changelog

## Unreleased

**The Action can use the history too**, via a new `cache-dir` input and an
`actions/cache` step. Without it the Action was the one place the cache could
not help — a fresh runner every time, re-paying for the same history on every
pull request — which is backwards, because Actions is exactly where the shared
request budget makes a month unreachable.

**`RAN` counts every execution again.** Steps finishing inside a second measure
as zero (step timestamps have no sub-second component) and were being dropped
from the count entirely, so the column silently meant "ran for at least a
second". Against `gohugoio/hugo` that produced `Install Go` 154 against its own
post step 86 — an impossible pair that was 68 dropped teardowns.


**History now outlives the process** ([#3](https://github.com/karanmonu/flakewatch/issues/3)).
Runs and their job data are kept in a local append-only file, so a run measured
once is measured forever and `-since` can cover a window no single invocation
could afford.

The arithmetic that makes this necessary: measured against `gohugoio/hugo`,
`-runs 200` bought 13.5 days and reaching 30 needed 445 requests. Inside GitHub
Actions the automatic token allows roughly 1,000 requests an hour for the whole
repository, shared with every other workflow — so a month of history was not
merely expensive there, it was structurally out of reach. Run daily and a real
month accumulates for the price of each day's new runs.

Safe because a completed run never changes: its jobs are finished, their
durations fixed, the runner labels they billed against settled. Only completed
runs are stored, for the same reason.

The report says how many runs came from history rather than from this
invocation's API calls, because a total assembled partly from stored history
covers more time than the requests made would suggest, and the reader should not
have to work that out. `-no-cache` disables it; `-cache-dir` moves it.

Without `-since` history is used to skip re-fetching but **not** to widen the
sample — silently reporting on 900 runs when `-runs 200` was asked for is a
different measurement than the one requested.


**A workflow that fails every time is no longer reported as stable.** The
flakiness score is zero for something consistently broken — that is what the
`4p(1-p)` term is for — but zero fell through to the "stable" branch, so a
workflow failing 100% of its runs came out with a green dot and a clean bill of
health. Found by pointing v0.5.0 at `gohugoio/hugo`, where a stale-issue
workflow had failed all thirteen times in the window and the report called it
fine. "Not flaky" and "not broken" are different claims and the output now
keeps them apart, with `⛔ always failing` above a 90% failure rate.

## v0.5.0

**You can supply your own runner rates** ([#15](https://github.com/karanmonu/flakewatch/issues/15)).
`-rates rates.json` takes a JSON object of runner label to USD per minute and
uses it ahead of both the published table and the self-hosted rule. This closes
the gap that mattered most: jobs on self-hosted runners and on labels with no
published rate were excluded from the total, so flakewatch undercounted hardest
for exactly the people with the largest bill. When labels are still unpriced the
report now prints the rate file you would need, with your own labels already
filled in, instead of only naming the gap.

**Runs that kept going after a newer commit replaced them** ([#8](https://github.com/karanmonu/flakewatch/issues/8)).
Pull request runs that were still executing when the next push started its own
run, and finished anyway. Priced from the moment a concurrency group would have
cancelled them rather than for the whole run, because the minutes before the
newer push were buying a result somebody still wanted. The report prints the
four lines of YAML that stop it. Pull request events only — two overlapping runs
on the default branch are one build per commit, which is the point of them.

**Where the time goes, by step** ([#9](https://github.com/karanmonu/flakewatch/issues/9)).
"Your Test workflow is 93% of the bill" was half an answer; the next question is
always which part of Test. The jobs endpoint already returns every step with its
timestamps in the response the cost arithmetic was reading anyway, so the
drill-down costs no extra requests. Matrix legs count separately, because a
four-second step fanned out twelve ways is not a four-second step.

Step figures are a *share* of a job's cost, not a charge: GitHub bills the job,
rounded up to the minute, so the steps sum to slightly less than the job. The
report says so next to the table rather than in a footnote.

**A workflow is now identified by its file, not its display name.** Everything
grouped runs by `name:`, which gets two common cases wrong: two files sharing a
name (`name: CI` is not rare) merged into one row whose cost was the sum of both,
and a workflow renamed mid-window split into two rows each holding half its
history and a flakiness score computed over half the evidence. The code already
argued that "names collide and get renamed" when matching `-changed` against
paths; it just did not apply that anywhere else. Falls back to the name for runs
old enough to predate GitHub returning a path.

**Cost and flakiness no longer count different things under one label.**
`WorkflowStats` carries `Runs` (every completed run, including cancelled ones,
because they cost money) and `Scored` (the subset that concluded success or
failure, the only subset a flakiness score can be computed over). The terminal
and the pull request comment show both, and the paragraph explaining why two
numbers disagreed is gone, because they no longer do ([#10](https://github.com/karanmonu/flakewatch/issues/10)).

**`context.Context` throughout.** Every request-making method takes one, requests
are built with `http.NewRequestWithContext`, and the CLI derives its context from
`signal.NotifyContext` — so Ctrl-C cancels in-flight requests instead of waiting
for 800 of them to drain, and the process exits 130 so a wrapping script can tell
an interrupt from a failure.

**The rate table detects its own staleness.** A retrieval date is honest but
passive; nobody reads a date and does the subtraction. Past six months the report
says how old the prices are and that the totals are indicative.

- `-changed` marks rows in terminal output too. It was a silent no-op outside
  `-markdown`.
- API errors carry GitHub's own message. A bare "403" left the reader guessing
  between a missing scope, SAML enforcement, and a repository that does not
  exist. Transport and decode failures are wrapped with `%w` and the URL.
- `max` no longer shadows the Go builtin of the same name; the parameter is
  `limit`.

## v0.4.0

**A monthly figure now requires at least a week of measured history.** CI load
is weekly-periodic, so scaling a shorter window misstates the month by whenever
you happened to run it. Surveying eight public repositories made the size of the
error concrete: golangci-lint measured $8.82 over 1.8 consecutive weekdays,
which the previous 24-hour floor scaled to "$143/month". The $8.82 was measured;
the $143 was an artefact of the sample. Seven of those eight repositories now
get no monthly figure at 50 runs, and are told why.

- A truncated window now says what `-runs` value would have covered the window
  you asked for, derived from the run density actually observed.
- The two run counts on the page reconcile. Cost includes every run that had
  jobs; the flakiness table counts only runs that concluded success or failure.
  Both are right, and the report now explains the gap instead of leaving a
  reader to notice it and distrust everything else ([#10](https://github.com/karanmonu/flakewatch/issues/10)).
- `.github/workflows/survey.yml` runs the tool against eight well-known public
  repositories on demand, so the README quotes measured output and anyone can
  reproduce it.

## v0.3.0

**The pull request comment is about the pull request.** The workflows a PR
touches are named in the lead sentence, sorted to the top, marked in the table
and never truncated out of it, with the rest of CI underneath for comparison.
Matching is on the workflow file path rather than the display name, because
names collide and get renamed while the path is what a diff gives you.

**`-since` fixes the window instead of the sample size.** A run count covers
however many days a repository happened to be busy for, which is why two samples
days apart produced $104/mo and $224/mo ([#11](https://github.com/karanmonu/flakewatch/issues/11)).
GitHub's `created` filter is date-granular, so the edge of the window is trimmed
to the exact instant.

**The Action will not fail your build.** Every failure path — an unreachable
release asset, a bad token, a fork's read-only token — became a workflow
annotation and a green check.

**The Action will not spend your API budget.** Inside Actions the `GITHUB_TOKEN`
allows roughly 1,000 requests an hour, shared with every other workflow in the
repository. The old default of 200 runs cost about 201 requests per pull
request. flakewatch now reads its remaining budget from the response headers,
keeps 100 requests in reserve, and shrinks the sample rather than spending the
lot. The default is 50 runs over a 30-day window.

A 403 is only treated as rate limiting when the remaining count is zero; GitHub
uses 403 for both "you may not" and "you have asked too often".

Fixed: the "largest single line" sentence read the top table row, which became
wrong once touched workflows began sorting above costlier ones.

## v0.2.0

Cost attribution per workflow, and a GitHub Action that comments it on pull
requests touching `.github/workflows/`.

Costs come from the jobs endpoint rather than the run timing endpoint. Calling
both turned up two reasons why: timing reports zero billable milliseconds for
public repositories, and it only distinguishes UBUNTU/MACOS/WINDOWS, so a
32-core job is indistinguishable from a 2-core one ([#6](https://github.com/karanmonu/flakewatch/issues/6)).

Billing rounds each *job* up to the whole minute, not the run, so cost is
`sum(ceil(job_ms))`. A run of ten 30-second jobs bills ten minutes, not five.

Spend on platforms dearer than Linux is reported with the Linux counterfactual
priced. Framed as an observation: flakewatch can see that a workflow spends on
macOS, not whether it needs macOS.

## v0.1.0

Flakiness scoring from pass/fail transition rate, zombie run detection, and
JSON output. Workflows with fewer than five runs get no score — two runs that
differ is one coin flip, and reporting that as maximal flakiness was a real bug.
