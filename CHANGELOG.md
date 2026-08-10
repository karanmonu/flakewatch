# Changelog

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
