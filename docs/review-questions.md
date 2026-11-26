# Review questions

**These are the seven questions we want a Kubernetes engineer to answer about kray.** Each has the context you need, the file to read, and what the code does today, so you can answer without a meeting. Short answers are fine. "This is wrong, here's a case" is the most useful answer.

Start with the [review guide](review-guide.md) if you haven't read it. Known gaps are in [known-limits.md](known-limits.md), and the cause rules are in [rules.md](rules.md).

## 1. Is the condition polarity table right, and what's missing?

**Context.** `describe`, and the `CAUSE` column of `list`, report every condition in `status.conditions` that isn't in its healthy state, for any kind, custom resources included. Whether `True` or `False` is healthy comes from a table, not from the kind. A wrong row makes kray report a problem on a healthy object, or miss a real one.

**Look at:** `internal/shape/conditions.go` (the tables at the top, `ConditionHealthy`, `polarityKnown`, `Signals`) and `conditions_test.go` (`TestConditionPolarity`).

**What the code does today:**

- A type is lowercased and matched first against exact rows, then by substring against word lists.
- `healthyWhenFalse` lists types that name a problem: `ReplicaFailure`, `Failed`, `FailureTarget`, `MemoryPressure`, `DiskPressure`, `PIDPressure`, `NetworkUnavailable`, `Stalled`, `Degraded`, `Conflict`, `Unschedulable`, `NonStructuralSchema`, `Terminating`, `ScalingLimited`, `Suspended`, `DisruptionTarget`, `PodResizePending`, `PodResizeInProgress`, `Resizing`, `FileSystemResizePending`, and the node problem detector types (`KernelDeadlock`, `ReadonlyFilesystem`, `Frequent*Restart`, `CorruptDockerOverlay2`, `NTPProblem`, and others).
- A type that contains `failure`, `failed`, `pressure`, `unavailable`, `degraded`, `stalled`, `conflict`, `error`, `remaining`, `problem`, `deadlock`, `corrupt`, `readonly` or `frequent` is also healthy when `False`. This check runs before the healthy words, so a type that contains both kinds of word counts as a problem.
- `healthyWhenTrue` lists `Ready`, `Available`, `Bound`, `Accepted`, `Initialized`, `Progressing`, `ContainersReady`, `PodScheduled`, `ResolvedRefs`, `Programmed`, `Established`, `NamesAccepted`, `AbleToScale`, `ScalingActive`, `Healthy`, `Synced`, `Reconciled`, `Approved`, `Complete`, `SuccessCriteriaMet`, `ReadyForQuery` and `CertificateSigned`. A type that contains `ready`, `available`, `healthy`, `synced`, `accepted`, `bound`, `initialized`, `established`, `programmed` or `reconciled` is treated the same way.
- A type in no table and with no listed word has unknown polarity. `True` is healthy; `False` is only a warning (`condition TYPE is False`), never a failure.
- `Unknown` is never healthy. For every type, known or not, an `Unknown` status is reported as unhealthy.
- `Complete` and `SuccessCriteriaMet` are not a problem when `False`. `Progressing` with reason `ProgressDeadlineExceeded` is unhealthy whatever its status. `ScalingLimited` and `Suspended` are warnings, not failures. `Unused` never makes a problem line.
- Pods, Events and ConfigMaps get no condition signals; some Node, Deployment, ReplicaSet, Job and PodDisruptionBudget types are skipped because their row already reports them.

**Questions.** Which rows are wrong? Which condition types from the operators and cloud providers you run are missing, and what is their healthy value? Is "`Unknown` is always unhealthy" right, given controllers that set `Ready=Unknown` while they reconcile? Do the substring words match a type they shouldn't?

## 2. Is the room check for a pending pod correct, and when should it say nothing?

**Context.** For a pod that the scheduler can't place, `describe` (and the worst three rows of `list`) compares the pod's requests with the free room on each node. The sentence "node X has room" sends the reader to look for another reason, so a wrong one costs time.

**Look at:** `internal/tools/diagnose.go` (`schedulingCause`, `placementCause`, `roomCause`) and `internal/tools/placement.go` (`nodeAccepts`, `tolerates`, `expressionHolds`, `unevaluatedPlacement`).

**What the code does today:**

- `placementCause` parses the scheduler message (`N/M nodes are available: ...`) and restates every reason that isn't `Insufficient ...`, with the pod's tolerations and node selection. If the message names such reasons and no `Insufficient`, the room check doesn't run. The light diagnosis of other `list` rows uses only this step.
- `roomCause` returns nothing when the pod requests no CPU and no memory. The pod's request is the sum of its containers, or the largest init container if that is larger. Every init container is treated this way, native sidecars included, and `spec.overhead` isn't added.
- It reads up to 2,000 nodes and up to 5,000 pods. A node's used room is the sum of the container requests of the pods bound to it that haven't succeeded or failed. Init containers and overhead of those pods aren't counted. When the pod list was cut, the cause says it's an estimate.
- Cordoned nodes are skipped and counted. A node is skipped when `nodeAccepts` is false: a `NoSchedule` or `NoExecute` taint with no matching toleration (`PreferNoSchedule` is ignored), a `nodeSelector` label that doesn't match, or required node affinity whose terms all fail (`In`, `NotIn`, `Exists`, `DoesNotExist`).
- If every node that isn't cordoned is skipped, it says nothing and leaves the scheduler's reasons. If every node is cordoned, it says so.
- Otherwise it states, for memory and then CPU, whether the request is larger than any node's allocatable, or larger than the most free room on any node. Only CPU and memory are compared: not pod count, ephemeral storage, extended resources or volumes.
- "Node X has room" is printed only when nothing else was found and the pod has no rule that isn't evaluated. Not evaluated: `matchFields`, `Gt` or `Lt` in required node affinity, required pod affinity or anti-affinity, a topology spread constraint with `DoNotSchedule`, and a toleration with an unknown operator. The "most free on any node" sentences still print when such a rule exists.

**Questions.** Is the request and used room arithmetic right, in particular for native sidecars, init containers and overhead? Are taints and tolerations matched the way the scheduler matches them (cordoned nodes and pods that tolerate them, `NoExecute`, empty keys)? Which other cases should make it say nothing?

## 3. Is the ClusterRole that `init` creates the smallest that works?

**Context.** The read-only identity is the real boundary when an agent has other ways in. `kray init` applies it with the person's credentials; `kray init --dry-run` prints it without cluster access.

**Look at:** `internal/setup/manifests.go` (`Rules`, `Objects`), `internal/setup/init.go` (`Init`, `apply`), and `internal/setup/doctor.go` (`dangerous`, `reviewWrapper`). The rendered ClusterRole is in the README, under "Read-only identity".

**What the code does today:**

- `Rules` gives `get` and `list` on every kind in the allowlist, grouped by API group, plus `get` on `pods/log` and `get`, `list` on `metrics.k8s.io` `nodes` and `pods`. No `watch`, no wildcards, no `secrets`, no `serviceaccounts`. Events are the core group only.
- Custom resources from the config file at the time of `init` are added to the role with the same verbs.
- The role is bound cluster-wide with a ClusterRoleBinding. The ServiceAccount has `automountServiceAccountToken: false`.
- `apply` uses server-side apply with field manager `kray-init` and `Force: true`. The token comes from the TokenRequest API (8 hours by default, 10 minutes to 24 hours) and is written to `~/.config/kray/kubeconfig` with mode 0600.
- `doctor` checks a fixed list of dangerous permissions with `SelfSubjectAccessReview`, through a transport that allows POST to three review endpoints only.

**Questions.** Is anything in the role broader than the six tools need? Does any rule reach Secret data by another path: env literals in pods, ConfigMaps, `pods/log`, event messages, PersistentVolume fields, custom resources? Kray redacts its own output, but the token in the kubeconfig file can read the raw objects. Should the binding be per namespace, and is `Force: true` safe when a role of the same name exists?

## 4. Are the paging and per-call limits safe for a cluster with 10,000 pods?

**Context.** A read-only tool that overloads the control plane is still an incident. An agent can call tools in a fast loop.

**Look at:** `internal/kube/readonly/load.go` (`Load`), `internal/kube/kube.go` (`New`, `list`), `internal/tools/callreader.go`, `internal/tools/list.go` (`fetchCap`), `internal/tools/diagnose.go` (`schedulingPodCap`), `internal/tools/top.go` (`topListCap`), and `internal/cli/cli.go` (`runner`).

**What the code does today:**

- `Load` sets QPS 10 and burst 20 on the `rest.Config`. `kube.New` builds three clients from it (dynamic, typed, discovery). client-go builds one limiter per client when `RateLimiter` isn't set, so the three limiters are separate. Each CLI command is a new process with new limiters.
- Every tool call has a 20 second deadline. One call has at most 6 reads in flight, and the reads about related objects hold at most 3 of them. A repeat read in the same call gets the first answer; nothing is kept between calls.
- A list reads pages of 500 objects, at most 10 pages. `list`, `events`, a search by name, and most related reads stop at 2,000 objects (4 pages) and say the list was cut. The room check reads up to 5,000 pods and 2,000 nodes; `top` reads up to 5,000 pods. `logs` for a workload reads at most 30 pods.
- `describe` with a name alone makes one list for each of six kinds, and one more for pods if none match.
- No `resourceVersion` is set on a list. The transport would reject it, because it isn't on the query allowlist.
- `init` creates no FlowSchema or PriorityLevelConfiguration. The requests fall under the flow schemas the cluster already has for that identity.

**Questions.** With 10,000 pods, which call is the most expensive for the API server and etcd, and is it acceptable? Should lists use `resourceVersion=0` or another cache read? Should `init` create a FlowSchema that puts this identity in a low priority level?

## 5. Is there a GET path that changes state or leaks, and that the deny list doesn't cover?

**Context.** The transport is the guarantee that kray only reads. It must hold even when a tool has a bug, because the tool layer and RBAC are separate layers.

**Look at:** `internal/kube/readonly/check.go` (`Check`, `shape`, `resourcePath`) and the tables in `check_test.go`.

**What the code does today:**

- It allows `GET` only, with no body and a content length of 0. It rejects an `Upgrade` header, any `Sec-WebSocket-*` header, and `upgrade` in `Connection`.
- Scheme and host must equal the configured API server (case-insensitive). User info, an opaque part and a fragment are rejected. A path prefix of the server URL (for example `/k8s/clusters/c-1`) is required and removed before the next checks.
- The path must have no escaped form (`RawPath` empty), only ASCII letters, digits, `-`, `.`, `_` and `/`, be equal to its `path.Clean`, and have no empty or dot segments.
- A segment equal to `proxy`, `exec`, `attach`, `portforward` or `secrets`, in any case, is rejected. A substring is allowed, so `kube-proxy-abc` passes.
- Allowed shapes: `/version`, `/api`, `/apis`, `/apis/GROUP`, then under a version a list or get of a resource, cluster-scoped or namespaced, and `namespaces/NS/pods/NAME/log`. Every other subresource is rejected. The log shape checks the resource name `pods`, not the group.
- The transport doesn't check the resource against the kind allowlist: any resource in any group passes it. The allowlist is in the tool layer (`internal/kinds`) and in RBAC.
- Query keys must be on the allowlist (`labelSelector`, `fieldSelector`, `limit`, `continue`, `container`, `previous`, `sinceSeconds`, `tailLines`, `timestamps`, `limitBytes`, `timeout`), each once, with no `;`. Their values aren't inspected.

**Questions.** Is there a GET on a built-in or common aggregated API that has a side effect, or returns credentials, and that this shape allows? Should the transport also enforce the kind allowlist? Is anything missing from the deny list?

## 6. Which five faults do you debug most that kray says nothing about?

**Context.** kray names a cause only for faults its rules know. For everything else, the agent gets trimmed data and has to find the cause itself. We want to add the rules that matter most, in order.

**Look at:** [rules.md](rules.md), [architecture §7.4](architecture.md#74-diagnosis) and §7.7, and `podCauses` in `internal/tools/diagnose.go`.

**What the code does today:** it names causes for OOM kills at a limit or on the node, pods that can't be scheduled (scheduler reasons, plus the room check), services with no ready endpoints (selector mismatch, pods not ready, target port mismatch, scaled to 0), image pull failures (not found, access refused, unreachable, invalid name), missing ConfigMap or Secret references reported by the kubelet, readiness, liveness and startup probe failures, crash loops with the exit code and the last error line, and Multi-Attach volume errors. For any kind it reports conditions that aren't healthy, and one step along the references (ReplicaSets, claims, ConfigMaps, autoscalers, disruption budgets, quotas, NetworkPolicies, Ingress backends).

**Question.** Name the five faults you debug most often that aren't in that list, with what you'd read to find each cause.

## 7. Where does a cause state more than its evidence supports?

**Context.** A wrong cause is worse than none: an agent trusts it and stops looking. We'd rather a rule say nothing than overreach.

**Look at:** `internal/tools/diagnose.go`, mainly `podCauses`, `oomRunTime`, `imagePullCause` and `roomCause`.

**What the code does today, in the places we're least sure of:**

- **OOM with no memory limit.** `podCauses` says "the node ran out of memory".
- **OOM run time.** `oomRunTime` says a kill less than 2 minutes after the start "points to a limit that is too low for the workload, not to a slow leak", and a kill after an hour or more "fits a slow leak or a load that grew".
- **Multi-Attach.** The cause says the volume is ReadWriteOnce and that a rolling update starts the new pod before it stops the old one. It reads the `FailedAttachVolume` event and the pod's claims, not the claim's access mode or the owner's strategy.
- **Liveness restarts with exit code 0.** The cause says "the application itself does not crash".
- **Image pull.** `imagePullCause` matches substrings of the kubelet message and events: `401` or `403` anywhere means access refused, and `x509` or `tls:` means the registry "is not reachable from the node".
- **CPU at limit.** "Throttling likely" is an inference: the metrics API has no throttling counters.

**Questions.** Which of these, or which other cause, states more than the evidence it read? For each, what should it say, or should it say nothing?
