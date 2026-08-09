# ADR 0001: Standard library only

## Status
Accepted

## Context
CLI tools in the Go ecosystem commonly pull in cobra, viper, and an API client
library, adding ~40 transitive dependencies for flag parsing and HTTP calls.

## Decision
flakewatch uses only the Go standard library: `flag` for CLI parsing,
`net/http` + `encoding/json` for the GitHub API.

## Consequences
- Zero supply-chain surface; `go install` is instant.
- Trivially auditable by security-conscious adopters.
- Cost: subcommands and shell completion will need manual work if added later.
  Revisit if the CLI grows beyond ~5 flags.

