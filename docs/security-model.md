# Security model

**No tool can write to the cluster, read a Secret, or change which cluster it talks to.** Tests enforce each rule below; see [Testing](testing.md) and [docs/architecture.md](architecture.md).

1. **Read only by construction.** Every request goes through one transport. It allows only GET, with no body and no protocol upgrade, to the configured API server, on paths without `proxy`, `exec`, `attach`, `portforward`, or `secrets`. There's no code path for a write.
2. **No shelling out.** kray uses the Kubernetes client library. It never builds a `kubectl` or `helm` command line.
3. **Six tools.** There's no passthrough, no exec, and no port forward.
4. **Stdio only.** There's no HTTP or SSE transport and no listening port.
5. **Never reads Secret objects.** References to them appear by name only.
6. **Never uses `nodes/proxy`.** A GET on `nodes/proxy` can run commands in pods.
7. **No telemetry, no LLM calls, and no network calls except to the cluster.**

## Read-only identity

**`kray init` creates an identity that can only get and list the allowed kinds.** It creates a Namespace, a ServiceAccount, a ClusterRole, and a binding. Then it gets a token with the TokenRequest API and writes `~/.config/kray/kubeconfig` (mode 0600). The token expires in 8 hours by default; `--duration` accepts 10m to 24h. `kray serve` uses that kubeconfig when it exists.

- **If your security team applies the manifests itself,** run `kray init --dry-run`. It prints the manifests without cluster access. After they're applied, run `kubectl create token kray-reader -n kray --duration 8h | kray init --token-stdin`.
- **If the token expires,** run `kray init --refresh`. Your agent can't do this.
- **To check the identity,** run `kray doctor`. It shows who kray runs as, when the token expires, and whether the metrics API is available. It exits with 1 if the identity can do more than read.

Without `init`, kray uses your kubeconfig identity. `kray serve` prints a warning, and a CLI command prints it when the output goes to a terminal. The tools stay read only either way.

<details>
<summary>The ClusterRole (<code>kray init --dry-run</code>)</summary>

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kray-reader
rules:
- apiGroups: [""]
  resources: [configmaps, events, limitranges, namespaces, nodes, persistentvolumeclaims,
              persistentvolumes, pods, resourcequotas, services]
  verbs: [get, list]
- apiGroups: [apps]
  resources: [daemonsets, deployments, replicasets, statefulsets]
  verbs: [get, list]
- apiGroups: [autoscaling]
  resources: [horizontalpodautoscalers]
  verbs: [get, list]
- apiGroups: [batch]
  resources: [cronjobs, jobs]
  verbs: [get, list]
- apiGroups: [discovery.k8s.io]
  resources: [endpointslices]
  verbs: [get, list]
- apiGroups: [networking.k8s.io]
  resources: [ingressclasses, ingresses, networkpolicies]
  verbs: [get, list]
- apiGroups: [policy]
  resources: [poddisruptionbudgets]
  verbs: [get, list]
- apiGroups: [storage.k8s.io]
  resources: [storageclasses]
  verbs: [get, list]
- apiGroups: [""]
  resources: [pods/log]
  verbs: [get]
- apiGroups: [metrics.k8s.io]
  resources: [nodes, pods]
  verbs: [get, list]
```

`init` also creates the Namespace `kray`, the ServiceAccount `kray-reader` (with `automountServiceAccountToken: false`), and a ClusterRoleBinding. There are no Secrets, no watch, and no wildcards.

</details>

## Audit log

**kray writes one JSON line per call to `~/.local/state/kray/audit.jsonl`.** The path follows `$XDG_STATE_HOME`; on Windows it's `%LocalAppData%\kray`. The file mode is 0600.

Each line records the tool, the redacted arguments, the context, namespace, and identity, the result size, the redaction count, blocked requests, the duration, and any error. It never holds result content. If kray can't write the line, the call fails, and `serve` won't start without an audit log.

```json
{"time":"2026-11-13T09:12:44.183Z","surface":"mcp","tool":"logs","args":{"namespace":"shop","pod":"api-7f9c8-x2k","previous":true},"context":"prod-eu","namespace":"shop","identity":"system:serviceaccount:kray:kray-reader","result_bytes":9214,"truncated":true,"redactions":3,"blocked_requests":0,"duration_ms":182,"error":"","session":"b41c09d2e5a7f318","version":"0.1.0"}
```
