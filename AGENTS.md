# Agent and contributor guide

This repository holds **kray**: one Go binary that gives an AI agent read-only
access to a Kubernetes cluster over MCP (stdio), with secrets redacted, every
call logged, and output shaped for a model.

Read [`docs/architecture.md`](docs/architecture.md) before you change code. It
is the authoritative design, and it lists the numbered invariants (I-1, I-2,
...).

## The rules that do not bend

The product is trust. One leak or one write is worse than a missing feature.
When safety and convenience conflict, choose safety and write down what you
gave up (in the PR description and, if it is lasting, in
`docs/architecture.md`).

1. **Read-only by construction.** Every request to the cluster from the
   server process goes through `internal/kube/readonly`. It allows HTTP GET
   only and rejects `proxy`, `exec`, `attach`, `portforward` and `secrets`
   paths. Do not create a Kubernetes client any other way.
2. **No shelling out.** Use client-go. Never build a `kubectl` or `helm`
   command line. Kray packages do not import `os/exec`.
3. **Six tools only:** `list`, `get`, `describe`, `logs`, `events`, `top`.
   No passthrough, no exec, no port-forward.
4. **Stdio only.** No HTTP or SSE transport. No listening port.
5. **Never read Secret objects.** No API call for secrets, at all.
6. **Never use `nodes/proxy`** (or any `proxy` subresource).
7. **No telemetry, no network calls except to the cluster, no LLM calls.**

If you think one of these rules has to bend, stop and ask a maintainer first.
Do not work around a rule in code. The guard tests in `internal/guard` fail the
build when a rule is broken in a way a test can see.

## Workflow (ATDD)

This project follows ATDD:

1. Write the failing acceptance test that encodes the behavior first.
2. Stop and get explicit human approval before you write the implementation.
3. Implement in small commits. Each commit has its tests.

For safety behavior, the acceptance test is usually a case in the canary
harness (`test/harness`) or a transport case in `internal/kube/readonly`.

## Branch, commit and PR conventions

- Branch off a fresh `origin/main`.
- Branch names, commit messages and PR titles say what the change does.
- Every PR description answers the safety checklist:
  - Does this change add a new path to the cluster? It must go through
    `internal/kube/readonly`, and the transport tests must cover it.
  - Does this change add a new output field or a new kind? Redaction must
    cover it, and the canary fixture must plant a canary in it.
  - Does this change add a dependency? Say why. Run `make vulncheck`.
  - What did you give up for safety?
- Tool output never mentions any product, including kray itself. Hints name
  tools and arguments only (for example: `call logs with tail=500`).

## Layout

```
main.go                    entry point at the module root (go install names the binary)
internal/brand/            the product name constant and build version (rename here)
internal/cli/              subcommands: serve, list, get, describe, logs, events, top, init, doctor, version
internal/mcpserver/        MCP server over stdio: six tools, server instructions, result framing
internal/tools/            the six tools and the diagnosis rules (diagnose.go); they see only the kube.Reader interface
internal/kube/readonly/    THE choke point: GET-only RoundTripper and the only rest.Config builder
internal/kube/             kube.Reader over client-go dynamic + discovery, built from readonly only
internal/kinds/            resource-kind allowlist and opt-in custom resources
internal/shape/            trimming, health, problem detection, size budget and truncation notes
internal/redact/           redaction engine: key names, value patterns, entropy; marks and counts
internal/audit/            JSON-lines audit log (never result content)
internal/config/           config file loading and validation
internal/setup/            init and doctor; the only package with non-GET clients; never imported by the server
internal/guard/            tests that enforce the rules above (imports, calls, transport use)
test/harness/              canary harness; targets any MCP server command (stub/ is a deliberately unsafe server)
test/fixture/              the canary fixture, shared by e2e and integration
test/e2e/                  the real binary through the harness against a fake API server (no Docker)
test/integration/          kind-based integration tests (build tag: integration)
bench/                     output-size benchmark against kubectl
docs/                      architecture, usage, security model, benchmarks, open questions
```

## Building and testing

### Hermetic checks (no cluster)

Run these first for every Go change:

```
make lint            # gofmt, go vet, staticcheck, govulncheck
make test            # go test ./... (includes test/e2e: the binary through the harness)
make test-race       # go test -race ./...
make build           # bin/kray
```

`make tidy-check`, `make shellcheck` and `make actionlint` run in CI lint too.

### Integration (kind)

The integration test needs Docker. It creates a kind cluster with API server
audit logging and metrics-server, applies the canary fixture, and runs the
canary harness against kray:

```
make kind-up         # cluster "kray" with audit logging
make integration     # go test -tags integration ./test/integration/...
make kind-down
```

To point the harness at another MCP server:

```
make harness TARGET="npx -y some-mcp-server"
```

If Docker or kind is not available, say so. Never report that the integration
test passed when it did not run.

### Benchmark

```
make bench           # needs kubectl and the kind fixture; writes bench/RESULTS.md
```

## Releasing

Cut releases with the **Release** workflow, not a hand-pushed tag:
`gh workflow run release.yml` (next patch after the latest `v*` tag) or
`gh workflow run release.yml -f version=X.Y.Z`. It tags main's HEAD only
after `ci` passed on that commit, then builds the binaries and checksums and
publishes a GitHub release.

## Cursor Cloud specific instructions

`scripts/cloud-agent-install.sh` installs the pinned Go, kind and kubectl, and
warms the module cache. `scripts/cloud-agent-start.sh` starts dockerd on every
boot, because the VM has no systemd.
