# Go correctness defaults

Use the shipped profile and `scripts/check-go.sh` as the starting point for Go modules. Keep the copied files in the repository, review upgrades, and run the same full command locally and in CI. Existing project policy remains authoritative; reconcile stronger checks rather than replacing them.

## Protections

| Area | Enforcement |
| --- | --- |
| Enums | `exhaustive` checks switches and enum-keyed map literals. A `default` does not cover omitted constants; enum switches also require a runtime fallback. |
| Sum types | `gochecksumtype` checks type switches over interfaces marked `//sumtype:decl`. A `default` does not cover omitted variants. |
| Errors | Strict `errcheck` checks discarded errors, blank assignments, and unchecked assertions. `errorlint`, `nilerr`, and `nilnesserr` check common error-handling mistakes. |
| Lifetimes and cancellation | HTTP body, SQL row/statement, tracing span, context propagation, and cancellation analyzers. |
| Security | `gosec`, Unicode identifier checks, and call-graph-based `govulncheck`. |
| Concurrency | Vet's copy-lock, atomic, and cancellation checks; context checks; uncached `-race` tests on executed paths. |
| Readability | `wsl_v5` permits one adjacent setup statement, compact exits for blocks up to two lines, and no mandatory trailing blank line for every switch case. |
| Tests and diagnostics | Tests are linted; test assertion and parallelism checks are enabled. All findings are reported without per-linter or duplicate caps. |
| Reproducibility | Pinned tools, strict formatting, read-only lint module resolution, and `go mod tidy -diff`. |

The profile explicitly selects correctness checks. It does not enable every linter: line-length rules, mandatory comments, arbitrary complexity thresholds, and full-field initialization of every third-party struct can create churn without proving correctness. Add project-specific constraints where their invariants justify them. One formatter policy avoids conflicting style tools.

## Enums and variants

Use typed constants; keep an intentional zero value. Use stable explicit strings or numeric assignments for persisted and wire values instead of relying on reorderable `iota` positions. Parse and validate untrusted inputs at boundaries: lint checks declared constants, not the entire underlying integer or string domain.

List all declared cases explicitly, including Unknown/Invalid if declared, then return an error or otherwise deliberately handle values outside the set. Intentional subsets need a local exception with a reason; a sparse lookup does not become a total domain function merely because its key type is an enum.

Use the following shape when variants carry different data:

```go
//sumtype:decl
type Result interface {
	isResult()
}

type Accepted struct{ ID string }
type Rejected struct{ Reason string }

func (Accepted) isResult() {}
func (Rejected) isResult() {}
```

Keep concrete variants in the declaring package and handle nil explicitly when allowed by the API. The marker pattern and linter approximate a closed sum type; Go does not provide native sealed types, and interfaces can still carry nil or be embedded elsewhere. Do not claim a compiler-level closed-world guarantee.

## Project verification

This tool owns its Go and shell verification runners. Run `scripts/check-source-code.sh go` for its versioned lifecycle plan, and `scripts/test.sh` for native acceptance coverage. The strict lint policy is `.golangci.yml`; there is no grandfathered baseline or separate legacy policy. Keep formatting, module tidiness, all configured linters, uncached race tests, and reachable-vulnerability analysis enabled.

Run `bash scripts/check-go.sh lint` while editing. Use `fmt path/to/file.go` for formatting and `fix ./path/to/package` for available cuddling fixes; inspect the diff. Lint includes integration-tagged acceptance code. The full gate runs lint first, then race tests and vulnerability analysis. The `test`, `vuln`, and `tools` modes isolate work. Do not add changed-line filtering or suppressions to silence new findings.

Every runner selects the Go version declared in `go.mod` and sets `GOWORK=off` so parent workspaces cannot replace dependencies. The runner pins golangci-lint 2.13.2 and govulncheck v1.9.0, verifies the official linter release checksum, and keeps tool dependencies outside the application module. Reuse the normal tool-download cache. Race checks need a C compiler and cgo; runtime binaries remain static. Initial tool installation and vulnerability analysis need network access.

The Go profile and copied guidance are project-owned native adaptations. Review any source-code setup or upgrade proposal before applying it; upstream templates can introduce different runners or dependencies. Preserve the current beta/tool obligations and the project's independent acceptance checks.

## Limits and sources

Lint catches known code patterns. Tests need independent expected behavior. The race detector covers paths actually run, and `govulncheck` detects known vulnerabilities reachable in the analyzed configuration. None proves domain correctness or rules out all concurrency or security defects.

- [Golangci-lint configuration](https://golangci-lint.run/docs/configuration/file/) and [linter settings](https://golangci-lint.run/docs/linters/configuration/).
- [Go race detector](https://go.dev/doc/articles/race_detector).
- [Go vulnerability management](https://go.dev/doc/security/vuln/).
