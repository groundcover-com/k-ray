# Security policy

kray gives AI agents read-only access to Kubernetes clusters. Its value is
trust, so we treat every safety defect as a security issue.

## Report a vulnerability

Do not open a public issue for a security problem.

Send the report to: **support@groundcover.com**. Put "kray security" in the
subject.

A redaction miss is a security report. If a secret value appears in tool
output or in the audit log, report it here, not in a public issue.

Include:

- The kray version (`kray version`) and the Kubernetes version.
- The exact tool call or command, and the output that shows the problem.
- Whether the cluster identity was the one that `kray init` creates, or a
  broader kubeconfig identity.
- For a redaction miss: the rule name you expected (for example `jwt` or
  `url-password`, as in `[REDACTED:<rule>]`), or "none" if no rule fits, and
  the shape of the value: where it was (a log line, an environment value, an
  annotation, a command argument), its length, its prefix or format (for
  example "`ghp_` and 36 letters and digits"), and the text around it.

Never send the secret value itself, not in the report and not in an
attachment. Replace it with a made-up value of the same shape, and rotate
the real secret: it was in front of an agent.

We reply within 3 working days. We publish a fix and a GitHub security
advisory, and we credit the reporter unless they ask us not to.

## What we treat as a vulnerability

- Any request other than HTTP GET that leaves the kray server process.
- Any request to a Secret, or to a `proxy`, `exec`, `attach` or `portforward`
  path.
- Any way for tool arguments to change which API path kray calls, beyond the
  documented kinds and names.
- A secret value in tool output or in the audit log when the value matches a
  documented redaction rule.
- A listening network port, or a network call to a host other than the
  cluster API server.
- Content from the cluster that kray presents as anything other than data.

## Known limits (not vulnerabilities, but reports are welcome)

- Redaction is pattern-based. It does not find every secret. A secret in an
  unusual format can appear in output. Report it the same way (above, with
  the shape, never the value), so that a rule can cover it.
- kray does not limit an agent that has its own shell and its own credentials.
  The real boundary is the read-only cluster identity that `kray init` creates.

## Verify a release

Every release has `SHA256SUMS` (the checksums of the binaries and their
SBOMs), `SHA256SUMS.sigstore.json` (a cosign keyless signature of
`SHA256SUMS`), and a build provenance attestation on GitHub. Before you run a
binary, check all three. You need `gh` and `cosign` v3 or later.

```sh
VERSION=0.0.1
FILE="kray_${VERSION}_linux_amd64"   # or darwin_arm64, and so on
gh release download "v${VERSION}" --repo groundcover-com/kray \
  --pattern "${FILE}" --pattern SHA256SUMS --pattern SHA256SUMS.sigstore.json

# 1. SHA256SUMS was signed by the release workflow on main.
cosign verify-blob \
  --bundle SHA256SUMS.sigstore.json \
  --certificate-identity "https://github.com/groundcover-com/kray/.github/workflows/release.yml@refs/heads/main" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS

# 2. The binary matches SHA256SUMS (on macOS: shasum -a 256 --check ...).
sha256sum --check --ignore-missing SHA256SUMS

# 3. The binary was built by the release workflow from this repository.
gh attestation verify "${FILE}" --repo groundcover-com/kray \
  --signer-workflow groundcover-com/kray/.github/workflows/release.yml \
  --source-ref refs/heads/main
```

Steps 1 and 3 both use the public Sigstore instance, so their signing
certificates (the repository name, the workflow and the commit) are in the
public Rekor transparency log.

## Supported versions

Only the latest release gets security fixes until version 1.0.
