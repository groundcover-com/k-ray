# Testing

**Tests are the product: they prove each rule in [Security model](security-model.md).** `make test` runs:

- **The transport choke point.** It allows every request shape kray sends and blocks every write, upgrade, dangerous path, other host, and unknown query. It includes a fuzz test and runs real client-go clients against a fake API server.
- **Guards.** The source has no `os/exec`, no listener, one config builder, and no import path from the server to the setup code.
- **Redaction.** Corpora of values to redact and values to keep, regression cases from a security review, and a fuzz test.
- **Diagnosis.** 15 broken scenarios (OOM kill, no room on any node, selector mismatch, bad image, failing probe, crash loop, volume attach conflict, and more). Each must get its likely cause from one `describe` call.
- **The canary harness.** It runs the real binary over stdio against a fake cluster with a random secret hidden in 13 places: 402 calls, 302 of them hostile. It passes only if no canary appears anywhere, only GET requests leave the process, and every normal call is useful. A deliberately unsafe server fails the same harness.

`make kind-up && make integration` runs the harness on a kind cluster with API server audit logging, once with a cluster admin identity and once with the `init` identity. The harness accepts any MCP server command: `make harness TARGET="<command>"`.
