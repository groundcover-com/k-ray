# Open questions

Items here are not architectural commitments. The implementation must not make
either answer expensive until a decision moves the item into
[`architecture.md`](architecture.md).

## Resolved

- **OQ-1** (2026-11-13): path **segment** match. Moved into
  `architecture.md` I-3 and §5.2.
- **OQ-4** (2026-11-13): 8-hour default token, as proposed. Moved into
  `architecture.md` §11.1.
- **OQ-7** (2026-11-13): the audit log fails closed, as proposed. Moved into
  `architecture.md` I-10.
- **OQ-2** (2026-11-13): Apache-2.0, as proposed. `LICENSE` added.
- **OQ-3** (2026-11-25): keep Blacksmith. A pull request from an outside
  collaborator runs CI only after a maintainer approves it.
- **OQ-9** (2026-11-25): keep the trace export. It skips runs from forks.

The original text of the resolved items follows, for the record.

## OQ-1: Literal "path containing" or path segment match (resolved: segment)

Rule 1 says the transport rejects "any path containing proxy, exec, attach,
portforward or secrets". A literal substring match also rejects common,
harmless objects:

- every `kube-proxy-*` pod in `kube-system`,
- every object in an `external-secrets` namespace,
- pods such as `oauth2-proxy-*`, `envoy-proxy-*`, `secrets-store-csi-driver-*`.

Proposal: reject a request when any **path segment** is exactly one of these
words (case-insensitive), and also reject every path that is not clean (see
architecture.md, I-3). This blocks `pods/x/exec`, `nodes/x/proxy` and
`/api/v1/secrets`, and allows `pods/kube-proxy-abc`. An object whose whole
name is `proxy` or `secrets` is still blocked.

Cost of the literal rule: the agent cannot read kube-proxy pods or the
external-secrets operator, which are frequent debugging targets.
Cost of the segment rule: the rule text and the code are not word-for-word
the same. A test lists every blocked and allowed shape.

Decision (2026-11-13): segment match.

## OQ-2: License (resolved: Apache-2.0)

Proposal: Apache-2.0. Decision needed before the repository goes public.

## OQ-3: CI runners for a public repository (resolved: approval for outside collaborators)

CI uses Blacksmith runners for every job.
When the repository is public, pull requests from forks also run on these
runners. Options: keep Blacksmith with "require approval for all outside
collaborators", or move the jobs that run fork code to GitHub-hosted runners.

## OQ-4: Token lifetime for `kray init` (resolved: 8 hours)

Proposal: 8 hours by default, `--duration` up to 24 hours. A person runs
`kray init --refresh` to get a new token. The API server can cap the
lifetime lower (`--service-account-max-token-expiration`); kray shows the
real expiry. Longer tokens are more convenient and less safe.

## OQ-5: Rename scope

The product name lives in one constant (`internal/brand.Name`). The Go module
path and the repository name contain the name too, because `go install`
names the binary after the module path. A rename is one constant plus a
mechanical module-path change. Is that acceptable?

## OQ-6: RBAC kinds and ServiceAccounts in the default allowlist

The default allowlist leaves out Role, ClusterRole, their bindings and
ServiceAccount. They help to debug "forbidden" errors, but they also map the
cluster's attack surface for whoever reads the output. Proposal: leave them
out of v1; add them later as an opt-in.

## OQ-7: Audit log failure mode (resolved: fail closed)

Proposal: fail closed. If kray cannot write the audit line, the tool call
fails. The cost is that a full disk stops kray.

## OQ-8: Security contact (resolved: support@groundcover.com)

`SECURITY.md` sends reports to support@groundcover.com.

## OQ-9: CI trace export on a public repository (resolved: keep)

`groundcover-trace-export.yml` sends CI traces to groundcover. It skips runs
from forks. Keep it, or remove it before the repository goes public?

## OQ-10: Exec credential plugins without `kray init`

Without `kray init`, kray uses the current kubeconfig context. EKS, GKE and
AKS contexts often use an exec credential plugin, which client-go runs as a
child process. That command comes from the person's kubeconfig, never from
agent input. Proposal: allow it (otherwise kray does not work on managed
clusters without init), and print it in the startup warning. With
`kray init`, the kubeconfig has a static token and no plugin.

## OQ-11: A public signature of the release (resolved: cosign keyless)

A cosign keyless signature writes the repository name, the workflow path and
the commit to the public Rekor log, for good. The repository is public, so
the release signs SHA256SUMS with cosign keyless (see SECURITY.md).
