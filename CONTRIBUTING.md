# Contributing to flakewatch

Thanks for looking. The project has three rules that keep the numbers honest:

**1. Measured, not illustrative.** Every claim in the README comes from running
the tool against real public repositories, and `survey.yml` reproduces the
table. A PR that changes what flakewatch reports should come with output from
a real repo showing the before/after.

**2. A visible gap beats a confident wrong number.** Jobs we can't price get
named and excluded, never silently counted. Windows too short to project get
no monthly figure. If your change adds an estimate, it must also add the
condition under which the estimate refuses to print.

**3. It never fails the build it reports on.** Every failure path in Action
mode — bad token, missing release, fork permissions — becomes an annotation
and a green check. New failure paths follow the same rule.

## Getting started

```
go build ./...
go test ./...
export GITHUB_TOKEN=...   # any token with actions:read
go run . -repo cli/cli -runs 50 -cost
```

Stdlib only — no new dependencies without an issue discussing why. Reasoning
for past decisions lives in `docs/adr/`.

## Where help is wanted

The labeled issues are scoped for contribution: job-level flakiness scoring
(#1), duration regression detection (#2), and failure log clustering (#4).
Each states the acceptance test. Questions welcome on the issue before you
write code — cheaper for everyone.
