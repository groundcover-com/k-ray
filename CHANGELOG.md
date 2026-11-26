# Changelog

All notable changes to this project are in this file. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before 1.0,
only the latest release gets security fixes (see
[SECURITY.md](SECURITY.md#supported-versions)).

## [0.0.1] - 2026-11-26

The first public release.

### Safety

- All requests from the server to the cluster go through one transport. It
  sends HTTP GET only, to the configured API server only, on clean `/api`,
  `/apis` and `/version` paths. It blocks `proxy`, `exec`, `attach`,
  `portforward` and `secrets` paths, watch, log follow and connection
  upgrades.
- Secret objects are never read. Every spelling of `secrets` is rejected
  before a call goes to the cluster.
- A closed list of kinds. Custom resources are opt-in.
- Redaction of secret-shaped values in all output: sensitive key names,
  environment and header pairs, command flags, embedded JSON and YAML, PEM
  key blocks, JWTs, passwords in URLs, provider tokens with a known prefix,
  bearer and basic credentials, and high-entropy strings. Each value shows as
  `[REDACTED:<rule>]`, and a header line counts them.
- Redaction runs before output is cut, so a cut never shows part of a
  secret. A multi-line key block is masked as one block.
- Names of pods, nodes, namespaces and ARNs are kept, not redacted as random
  strings.
- Cluster content is framed as untrusted data, and the frame marker is
  escaped wherever it appears in cluster content.
- An audit log with one JSON line per call (never the result content). The
  server does not start without it, and a call fails when its audit line
  cannot be written.
- Fixes for nine findings of an independent security review, each with a
  regression test.
- Guard tests fail the build when a package imports `os/exec`, opens a
  listener, or builds a cluster client outside the read-only transport.
- `init` creates a read-only identity (get and list on the allowed kinds,
  pod logs, metrics; no watch, no Secrets, no wildcards) and writes a
  kubeconfig with a short-lived token. `doctor` reports the identity and
  exits with an error on any dangerous permission.

### Diagnosis

- `describe` names the likely cause of a problem, not only the symptoms:
  OOM kills (with the limit, the use and how long the container ran),
  pending pods (requests against nodes, taints, node selectors and affinity),
  services with no ready endpoints (selector against pod labels), image pull
  failures, failing readiness and liveness probes, crash loops (exit code and
  the last error line), volume attach conflicts, and init containers that
  wait for an address.
- Workloads show the cause of their worst pod, what changed in the current
  revision, and how the failing pods differ from the healthy ones.
- Resource verdicts from the metrics API: near OOM, CPU at the limit, no
  limits, over-provisioned.
- Unhealthy conditions for every kind, references followed one step, and a
  cause for every object that shares a name.
- Fewer false problems on healthy objects. A cause is given only where the
  evidence points to it.
- A read that failed is named in the output.

### Output size

- `list` puts unhealthy objects first, each with its likely cause, and counts
  healthy objects by workload. A large cluster keeps room for its problems.
- `get` returns about half of the object: no API defaults, healthy
  conditions folded, long values cut, with a note that says what was cut and
  which argument shows more.
- Log summaries put errors first, collapse repeated lines into patterns with
  counts and time ranges, show the spread of each variable, and show the
  message of a JSON log record.
- `logs` reads a whole workload and merges the lines of its pods.
- A `match` filter on `list`, `events` and `logs`.
- The cluster reads of one call run at the same time, and each read is made
  once.

### CLI

- Six tools, on the command line and over MCP on stdio: `list`, `get`,
  `describe`, `logs`, `events`, `top`. Commands: `serve`, `init`, `doctor`,
  `version`.
- The CLI accepts kubectl read commands (get, describe, logs, top, events,
  rollout status) and translates them to the six tools. It never runs
  kubectl.
- `describe` finds an object by name or selector, in any namespace when no
  namespace is given. `get` answers `-o jsonpath`.
- Next-step hints that work as written: flags on the command line, tool
  arguments for an agent.
- The identity warning shows only on a terminal.
- `make install` installs the binary to `~/.local/bin`.

### Benchmark

- `make bench-up`, `make bench-agents`, and `make bench-down` run the agent
  benchmark on a local kind cluster.
- A size benchmark of output bytes and estimated tokens against kubectl on
  the test cluster.
- An agent benchmark that gives the same root-cause tasks to the same model
  with kubectl, with an rtk proxy, and with this tool as a CLI and over MCP.
  It records calls, tokens, time, cost, the grade, and whether a planted
  secret reached the model.
- Hard fault sets, held-out and blind fault sets, a long-conversation mode,
  and a real-cluster mode that only reads.

### Tests

- A canary harness that can target any MCP server. It plants a canary in
  every place a secret can hide, sends normal and hostile calls, and fails
  when a canary appears in any output or audit line, or when the API server
  audit log shows any request other than get and list.
- An end-to-end test of the real binary against a fake API server (no
  Docker), and an integration test on a kind cluster with API server audit
  logging and metrics-server.
- A deliberately unsafe stub server that the harness must fail.

### Project

- Apache-2.0 license.
- CI: lint, unit tests with the race detector, builds for every release
  platform, and the kind integration test on three Kubernetes minors (1.35,
  1.36, 1.37). Dependabot for Go modules and GitHub Actions. Every action is
  pinned to a commit.
- Releases: binaries for Linux, macOS and Windows on amd64 and arm64, an
  SPDX SBOM for each binary, SHA256SUMS with a cosign keyless signature, and
  a build provenance attestation. SECURITY.md says how to verify them.

[Unreleased]: https://github.com/groundcover-com/kray/compare/v0.0.1...HEAD
[0.0.1]: https://github.com/groundcover-com/kray/releases/tag/v0.0.1
