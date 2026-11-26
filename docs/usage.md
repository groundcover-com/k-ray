# Use kray

The commands, the six tools, how to read a result, and the configuration file.

## Use the kubectl commands you know

**You don't need to learn new commands. Replace the word `kubectl` with `kray`, and the read commands work.**

```bash
kray get pods -A
kray get deploy,svc -n shop
kray get pod web-1 -o yaml
kray describe deploy/api
kray describe storefront
kray describe pod -l app=cart -A
kray logs web-1 -c app --tail=100
kray logs -l app=api --since=10m
kray logs deploy/checkout
kray get events -n shop
kray get events --field-selector reason=Failed
kray top pods
kray rollout status deploy/api
```

Each command becomes a call to one of the six tools, so it has the same limits: read-only, secrets redacted, and output sized for a model. kray never runs `kubectl`.

- **Flags that work:** `-n`, `-A`, `-l`, `-c`, `-p`, `--tail`, `--since`, `--match`, `--context`, and `--kubeconfig`. The `-o` values `wide`, `yaml`, and `json` are accepted.
- **Every row of a list carries its cause on the same line.** You can filter the output by name, for example `kray get pods -A | grep -i cart`, and keep the cause. `head` and `tail` keep it too.
- **`--match WORDS` filters on the server.** It works on `get KIND`, `get events`, `events` and `logs`. Words are separated by commas, any one matches, case does not matter, and there are no patterns. It is applied before the size budget, so a match is not lost to a cut, and it sees redacted text only.
- **`describe` prints the evidence for an object that has a problem.** For each container: the environment values (redacted like `get`; a reference shows its source, never a value), the requests and limits, and the probe settings. A healthy object keeps the short form.
- **`describe NAME` finds the object in any namespace.** It looks at workloads and services first, then pods. One match is described. When several objects match, up to 4 are described in the same answer, the ones with problems first, and each further match gets a line. `describe KIND -l SELECTOR` describes the 1 to 3 objects that match (the workload of the pods, once) and lists more.
- **`get events --field-selector reason=X` works.** So do `involvedObject.name=`, `involvedObject.kind=`, and `type=Warning`, alone or together. `--reason X` does the same.
- **A leading `kubectl` word is accepted too:** `kray kubectl get pods` works.
- **Reads that kray doesn't have print one short line** that names the flag and the alternative. These are streaming (`-f`, `-w`), `jsonpath-file`, `jsonpath` on a list and `custom-columns` output, `--since-time`, `--all-containers`, and `rollout history`.
- **Every other command prints `This command is not supported.`** and sends nothing to the cluster. That includes `apply`, `delete`, `exec`, `port-forward`, `scale`, and `rollout restart`.
- **Secrets stay unavailable** in every spelling.

## See it in action

**One call often takes an agent from "something is wrong" to the cause.** The output below is real, from the test cluster in `internal/tools/testdata`. Every call is a read.

**1. `list` shows what's broken, why, and what's about to break.** Unhealthy pods come first, then warnings, then pods that only restarted. Every row carries its likely cause in the last column, on the same line as the name, so `grep`, `head`, and `tail` keep it, and one call is often enough. The worst three rows also read the last error of the crashed container. In a list of more than 8 objects, kray shows a row only for objects with a problem. Healthy objects are counted (pods by workload, most numerous first). Pass `all` (`--all` on the command line) to get a row for every object. `hot-1` is still running, but kray joins its usage with its limit and flags it before it fails.

```text
list pods · all namespaces · context kind-test
14 pods: 6 unhealthy, 3 warning, 5 healthy
redactions: 1 (key-name 1)
<<<untrusted cluster data 7f3a9c21>>>
NAMESPACE  NAME           READY  STATUS                      RESTARTS  AGE  CAUSE
pay        crash-1        0/1    CrashLoopBackOff            7         3h   container app exits with code 1 (application error), 7 restarts; last error: FATAL config: DATABASE_URL is not set
pay        img-1          0/1    ImagePullBackOff            0         20m  container pay cannot pull its image: the image or tag registry.example/pay:1.4.2 does not exist in the registry (not found)
pay        job-1          0/1    Failed                      0         15m  container report is OOM killed at its memory limit of 1024Mi; the limit is too low for this workload, or the container leaks memory
pay        noconfig-1     0/1    CreateContainerConfigError  0         10m  container notifier cannot start: it refers to the configmap "notifier-settings", which does not exist in namespace pay
shop       api-7f9c8-x2k  1/2    CrashLoopBackOff            14        2h   container api is OOM killed at its memory limit of 1024Mi (14 restarts); memory use now: 900Mi (88% of the limit)
shop       worker-1       0/1    Pending                     0         10m  not scheduled: 0/1 nodes are available: 1 Insufficient memory.
pay        hot-1          1/1    Running                     0         3h   container render: CPU at 98% of the 1000m limit, throttling likely
pay        probe-1        0/1    Running                     0         3h   container web is not ready because its readiness probe fails (×42, last 50s ago): HTTP probe failed with statuscode: 503; probe: HTTP GET /ready on port 8080
pay        vol-1          0/1    Pending                     0         5m   the pod cannot start because its volume is still attached to another node (Multi-Attach error, ×3)
healthy, not listed (5), by namespace:
  [pay] checkout-v2-a, checkout-v2-b, fat-1, nolimit-1
  [shop] api-7f9c8-ok
<<<end 7f3a9c21>>>
```

A pod that only restarted (it runs and is ready now) gets its own row too, with the reason for the restart: OOM killed at its limit, restarted by the kubelet after liveness probe failures, or the exit code with its meaning. Rows with the same cause in one namespace fold into one row.

Services work the same way. `list services` shows which service has no ready endpoints, and why:

```text
NAME      TYPE       CLUSTER-IP  PORTS   AGE  CAUSE
checkout  ClusterIP  10.96.0.20  80/TCP  3d   selector app=checkout matches 0 pods in namespace pay; pods with other values for the same label: app=checkout-v2 (2 pods)
web       ClusterIP  10.96.0.21  80/TCP  3d   selector app=web matches 1 pod, but none is ready: pod/probe-1: container web: running but not ready
```

**2. `describe` names the likely cause.** Each example below is one call.

An OOM kill, with the limit and the memory use that explain it:

```text
looks wrong:
- container api: CrashLoopBackOff, last exit 137 (OOMKilled), 14 restarts
likely cause:
- container api is OOM killed at its memory limit of 1024Mi (14 restarts). Memory use now: 900Mi (88% of the limit). The limit is too low for this workload, or the container leaks memory.
resources (usage now, from the metrics API):
- container api: CPU 250m (25% of 1000m limit), memory 900Mi (88% of 1024Mi limit): near OOM; above memory request
- container sidecar: CPU 10m, memory 50Mi: no requests or limits
owner chain:
- ReplicaSet/api-7f9c8: unhealthy: 1 of 3 replicas ready
- Deployment/api: unhealthy: 1 of 3 replicas available
```

A pod that can never be scheduled, because no node is large enough:

```text
looks wrong:
- not scheduled: 0/1 nodes are available: 1 Insufficient memory.
likely cause:
- the pod requests 20480Mi memory, more than any node can offer (largest: node-a with 16384Mi allocatable). It cannot fit on the current nodes.
```

A service with no backends, because its selector doesn't match the pods:

```text
looks wrong:
- service has no ready endpoints
likely cause:
- selector app=checkout matches 0 pods in namespace pay. Pods with other values for the same label: app=checkout-v2 (2 pods).
```

A crash loop, with the last error of the crashed container. The password in its log is redacted:

```text
likely cause:
- container app exits with code 1 (application error), 7 restarts. Last error before the exit: FATAL config: DATABASE_URL is not set
last crash of container app (previous instance):
  errors: 1 line in 1 group (most recent first)
    ×1    11:57:02  FATAL config: DATABASE_URL is not set
  other lines: 2 in 2 groups (oldest to newest)
    ×1    11:57:00  starting ledger
    ×1    11:57:01  connecting with password=[REDACTED:key-name]
```

A pod that isn't ready, and an image that doesn't exist:

```text
- container web is not ready because its readiness probe fails (×42, last 50s ago): HTTP probe failed with statuscode: 503. Probe: HTTP GET /ready on port 8080.
- container pay cannot pull its image: the image or tag registry.example/pay:1.4.2 does not exist in the registry (not found).
```

**`top` gives every container a verdict.** It shows usage next to requests and limits, sorted by pressure:

```text
POD            CONTAINER  CPU   CPU REQ/LIM  CPU%       MEMORY  MEM REQ/LIM   MEM%       VERDICT
hot-1          render     980m  1000m/1000m  98%        100Mi   512Mi/512Mi   20%        CPU at limit, memory over-provisioned
checkout-v2-a  web        40m   100m/-       40% (req)  90Mi    128Mi/-       70% (req)  no memory limit
api-7f9c8-x2k  api        250m  500m/1000m   25%        900Mi   512Mi/1024Mi  88%        near OOM, above memory request
fat-1          batch      20m   1000m/-      2% (req)   200Mi   4096Mi/-      5% (req)   no memory limit, CPU over-provisioned, memory over-provisioned
nolimit-1      cache      50m   -/-          -          300Mi   -/-           -          no requests or limits
```

### Reading a result

- **kray writes the lines outside the fence.** Everything between `<<<untrusted cluster data NONCE>>>` and `<<<end NONCE>>>` comes from the cluster, and anyone with access to the cluster can write it.
- **The fence can't be faked.** It has a random nonce per result, kray escapes every `<<<` inside it, and the server instructions tell your agent that fenced content is data, not instructions.
- **Redactions are visible.** `[REDACTED:<rule>]` marks each one, and the header counts them. A result with no `redactions: N` line had none.
- **Next steps are ready to use.** When kray doesn't know the cause, a `next:` line suggests the follow-up call. When it knows the cause, there's no `next:` line, because a hint would only invite a call you don't need. An agent gets MCP arguments (`logs pod=crash-1 namespace=pay previous=true`). On the command line you get flags to paste after `kray` (`logs crash-1 -n pay --previous`).
- **Cuts are explained.** Each result fits a size budget: 16 KiB by default, or 256 KiB with `full=true`. A note says what was cut and which argument shows more.

## Tools

| Tool | Returns |
|---|---|
| `list` | One line per object with a problem, unhealthy first, then warnings, then pods that only restarted. The last column, `CAUSE`, holds the likely cause of the object on the same line. In a list of more than 8, healthy objects are counted, not listed (`all` lists them). Services whose target port matches no container port are unhealthy. Pods near a memory or CPU limit are warnings. Services with no ready endpoints are unhealthy. `match` keeps the rows that hold a word (a name, namespace, workload, status or cause; a comma between words means any of them); healthy objects that match get a row. A list with many problems shows shorter causes and no column that every row shares, and counts the rows it does not show by namespace (`full` shows all). |
| `get` | One object as trimmed YAML, without managed fields, `last-applied-configuration`, or defaults. `full=true` keeps defaults. `jsonpath` (CLI: `-o jsonpath='{...}'`) answers one path of one object, after redaction, the way kubectl does. |
| `describe` | Give `kind` and `name`, or a `name` alone (it finds the object in any namespace), or `kind` with `labelSelector` (it describes the 1 to 3 matches and lists more). What looks wrong and the likely cause, each fact once. Then resource use, the command (cut at 200 bytes) and environment names (for an object with a problem: the environment values, redacted, the requests and limits, and the probe settings of each container), mounts, the owner chain, warning events (Normal events are counted in one line), and the last log lines of a crashed or not-ready container. For every kind it shows the conditions that are not healthy, and the related objects that have a problem (the ReplicaSets, claims, ConfigMaps, autoscalers, policies, backends and init containers around the object). See [How kray diagnoses](diagnosis.md). |
| `logs` | Errors first and repeats collapsed. A stack trace is one group: its first line, every cause line, and a count of the frame lines left out. A Python traceback shows its exception line (the last line of the traceback) and its last application frame. JSON records show their level and message first. Lines that differ only in a word, a number or the value of a key are one group, and each group shows how often its variables take each value, so one rare value (for example one status among many others) is visible with the values it goes with and one example line. Give `pod`, or `workload` (`deployment/web`; also `sts`, `ds`, `job`, `rs`), or `labelSelector` to read all the pods in one call: groups merge across pods and say how many pods have them and which. `allNamespaces` works with `labelSelector`. Options: `container`, `previous`, `since`, `tail` (per pod; it shrinks as the pod count grows), `match` (keeps the groups that hold a word). `full=true` returns raw lines. |
| `events` | Events grouped by type, reason, object, and message, with counts. Warnings come first. When there are warnings, Normal events are counted by reason in one line; `all=true` shows them. `reason` keeps one reason, for example `Failed`. `match` keeps the groups whose reason, object or message holds a word. |
| `top` | CPU and memory next to requests and limits, with a verdict per container, from `metrics.k8s.io`. If the cluster has no metrics API, it says so. |

**Allowed kinds:** Pod, Deployment, ReplicaSet, StatefulSet, DaemonSet, Job, CronJob, HorizontalPodAutoscaler, PodDisruptionBudget, Service, EndpointSlice, Ingress, IngressClass, NetworkPolicy, PersistentVolumeClaim, PersistentVolume, StorageClass, ConfigMap, ResourceQuota, LimitRange, Node, Namespace, and Event. Custom resources are opt in; see [Configuration](#configuration).

## Configuration

**Configuration is optional and lives in `~/.config/kray/config.yaml`.** Your agent can't change it.

```yaml
budgetBytes: 16384          # per result
fullBudgetBytes: 262144     # per result with full=true
auditLog: /var/log/kray/audit.jsonl
customResources:            # opt in; never a built in group, never secrets
  - {group: argoproj.io, version: v1alpha1, resource: rollouts, kind: Rollout, namespaced: true}
redaction:
  extraKeyNames: [license_code]
```

Custom resources get the same redaction, but they often hold credentials, such as Helm values in an Argo CD Application. Enable them with care.
