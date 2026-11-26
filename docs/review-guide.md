# Review guide

**This guide gets a Kubernetes engineer from zero to a useful review of kray.** It says what kray is, where each safety rule is enforced, which files to read in which order, how to run every test, and what an earlier internal review already found. The questions we want you to answer are in [review-questions.md](review-questions.md).

## What kray is

kray is one Go binary that gives an AI agent read-only access to one Kubernetes cluster, as an MCP server over stdio and as a CLI. It has six tools (`list`, `get`, `describe`, `logs`, `events`, `top`), it never reads Secret objects, it redacts secret-shaped values in everything it prints, and it writes one audit line per call. It also names the likely cause of a fault with fixed rules that join facts `kubectl` shows in separate commands.

## The rules that do not bend, and where they're enforced

| Rule | Enforced in | Proven by |
|---|---|---|
| Read-only by construction: every request goes through one GET-only transport | `internal/kube/readonly/check.go` (`Check`), `transport.go` (`Wrapper`), `load.go` (`Load`, the only server `rest.Config` builder) | `check_test.go` (`TestCheckAllows`, `TestCheckBlocks`, `FuzzCheck`), `transport_test.go` (`TestLoadedClientsBlockEverythingElse`), `internal/guard` (`TestOneConfigBuilder`) |
| No shelling out | No `os/exec` in kray packages | `internal/guard` (`TestNoExecImports`; `os.StartProcess` in `TestNoListenersOrOwnNetwork`) |
| Six tools only | `internal/mcpserver/server.go` | `server_test.go` (`TestExactlySixReadOnlyTools`, `TestSchemasHaveNoTargetArguments`) |
| Stdio only, no listening port | No listener or HTTP server in the code | `internal/guard` (`TestNoListenersOrOwnNetwork`); the harness runs the server under `strace` on Linux |
| Never read Secret objects | `secrets` is a denied path segment in `check.go`; `internal/kinds` returns `ErrSecrets` for every spelling; config rejects a custom resource named `secrets` | `TestCheckBlocks`, `kinds_test.go` (`TestLookupRejectsSecretsInEverySpelling`) |
| Never use `nodes/proxy` | `proxy` is a denied segment, and every subresource except `pods/log` is rejected (`shape` in `check.go`) | `TestCheckBlocks` |
| No telemetry, no network except the cluster, no LLM calls | No `net.Dial`, `http.Client` or `http.Get` outside client-go; the transport pins the API server host | `TestNoListenersOrOwnNetwork`; `strace` in the integration harness |

`internal/setup` (`init` and `doctor`) is the only package with clients that send anything other than GET. `TestServerDoesNotImportSetup` in `internal/guard` proves the server never depends on it.

## The numbered invariants

The full text is in [architecture §3](architecture.md#3-invariants), and §14 names the test for each.

- **I-1** One choke point: every request goes through the read-only transport.
- **I-2** GET only, no body, no upgrade headers.
- **I-3** Safe paths only: the configured host, a clean path, no `proxy`, `exec`, `attach`, `portforward` or `secrets` segment.
- **I-4** Known query keys only; `watch` and `follow` are rejected.
- **I-5** Setup is separate: only `internal/setup` sends non-GET requests, and the server never imports it.
- **I-6** No `os/exec`, no listener.
- **I-7** Closed kind set; config adds custom resources only, never a built-in group or `secrets`.
- **I-8** Everything that leaves passes the redactor.
- **I-9** Cluster content sits inside a fence with a random nonce.
- **I-10** One audit line per call; the call fails if the line can't be written.
- **I-11** The cluster is fixed at start; no argument selects a context, server or credential.
- **I-12** Every result fits the size budget, and every cut says what it cut.
- **I-13** The MCP server registers exactly six tools.
- **I-14** The API server is the only network peer.
- **I-15** Secret references are shown by name and never resolved.

## Reading order

1. **`internal/kube/readonly`: the only path to the cluster.** Look for a GET that can still change state or leak, and for a request shape that `Check` accepts but shouldn't. Start at `Check` and `shape` in `check.go`, then the allow and block tables in `check_test.go`.
2. **`internal/redact`: the redaction engine.** Look for a secret format that neither the key-name pass nor the pattern pass catches, and for a place where text is cut before it's redacted. `redact.go` holds the rules; `lines.go` masks key blocks that span several log lines.
3. **`internal/guard`: the tests that enforce the rules.** Look for a way to break a rule that these AST and import scans don't see.
4. **`internal/tools/diagnose.go` and `internal/shape/conditions.go`: the cause rules.** Look for a cause that states more than its evidence, and for condition types whose polarity is wrong or missing. `podCauses`, `roomCause` (with `placement.go`) and `serviceCause` are the main rules; the polarity tables are at the top of `conditions.go`.
5. **`internal/setup`: `init` and `doctor`, the only package with non-GET clients.** Look at the ClusterRole that `Rules` in `manifests.go` builds, and at the POST allowlist of `reviewWrapper` in `doctor.go`.

## How to run everything

You need Go. The integration test also needs Docker and kind.

```bash
make lint            # gofmt, go vet, staticcheck, govulncheck
make test            # go test ./...; includes test/e2e, the real binary through the canary harness against a fake API server
make test-race       # go test -race ./...
make kind-up         # kind cluster "kray" with API server audit logging and metrics-server
make integration     # the canary harness against kind, with an admin identity and with the init identity
make kind-down
```

To run the canary harness against another MCP server, for comparison:

```bash
make harness TARGET="<mcp server command>"
```

The harness plants canary secrets in the fixture, makes normal and hostile calls, and fails if any canary appears in output, if a request other than GET leaves the process, or if a normal call isn't useful. `test/harness/stub` is a deliberately unsafe server that must fail it (`TestHarnessFailsAnUnsafeServer` in `test/e2e`).

## What an earlier internal review found and fixed

Please don't spend time on these again, but do tell us if a fix is incomplete.

- **Redact before every cut**. Several places cut cluster text before redacting it, so a quoted secret of several words lost its closing quote and kept its first word. Every cut site now redacts first, log lines are redacted as a sequence so a multi-line key block is masked whole, and command arguments are redacted per element.
- **Plant canaries of several parts**. Every canary was one token, so the harness couldn't see a leak of part of a secret. The fixture now plants a key block over several log lines, quoted phrases across the cut points, and a flag value with spaces, and the harness checks each part in every encoding.
- **Stop reporting problems on healthy objects**. Some rules printed a wrong cause or a problem on a healthy object: a running native sidecar, an old restart, a rolling update within its budget, a room check that counted nodes the pod can't use. Each fix has a test where the rule must stay silent (the `TestSilent...` tests in `internal/tools` and `internal/shape`).
- **Name a read that failed**. A failed read looked like an empty result, so `describe` could say "not found" or "no events" when it hadn't read them. Each answer now names what wasn't read, with a fixed reason and never the text of the API error.

## Further reading

- [Known limits](known-limits.md): what kray doesn't do, and where a cause can be wrong.
- [Rules](rules.md): the table of cause rules.
- [Architecture](architecture.md): the full design. §5 is the read-only boundary, §7.4 the diagnosis, §9 redaction, §12 what kray gives up for safety.
