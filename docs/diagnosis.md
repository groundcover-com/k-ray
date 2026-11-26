# How kray diagnoses

**kray joins facts that `kubectl` returns in separate commands, then states the likely cause and the evidence.** It uses fixed rules. It makes no LLM calls, and every input is a read.

| Symptom | What kray joins | What your agent gets |
|---|---|---|
| OOM kill | The memory limit and the memory use now | "OOM killed at its memory limit of 1024Mi. Memory use now: 900Mi (88%)" |
| Pending pod | The pod's requests and each node's free capacity | "requests 20480Mi memory, more than any node can offer" or "the most free on any node is 11008Mi" |
| Service with no endpoints | The selector and the labels of the pods | "selector `app=checkout` matches 0 pods. Pods with other values: `app=checkout-v2`" |
| Image pull failure | The image and the kubelet's error | Not found, access refused, or registry unreachable |
| Pod not ready | The probe and its failures in events | "readiness probe fails (×42): HTTP 503. Probe: HTTP GET /ready on port 8080" |
| Crash loop | The exit code and the previous instance's logs | "exits with code 1. Last error before the exit: ..." |
| Pod stuck before start | The volume attach failures in events and the pod's claims | "its volume is still attached to another node (Multi-Attach error). The volume attaches to one node at a time" |
| Unhealthy workload | The cause of its worst pod | The same cause, with the pod's name |

**Resource verdicts** come from the metrics API, joined with each container's requests and limits:

| Verdict | Rule | Shown as |
|---|---|---|
| near OOM | Memory at 85% or more of the limit | Warning in `list`, `top`, and `describe` |
| CPU at limit | CPU at 90% or more of the limit; throttling is likely | Warning in `list`, `top`, and `describe` |
| above memory request | Memory more than 10% above the request; evicted first under node pressure | `top` and `describe` |
| no memory limit, or no requests or limits | Nothing caps the container | `top` and `describe` |
| over-provisioned | The request is 4 or more times the usage | `top` and `describe` |

The idea of usage next to requests and limits comes from groundcover's [Murre](https://github.com/groundcover-com/murre). The method differs: Murre reads the kubelet through `nodes/proxy`, which kray never uses. kray reads `metrics.k8s.io`.

**What a diagnosis is, and isn't:**

- **It's a likely cause, not a certainty.** kray states what it read, so your agent can verify it with `logs`, `events`, or `get`.
- **It's a snapshot.** kray sees usage now, not history. A container that leaks slowly looks healthy until it's close to its limit.
- **It can't measure CPU throttling.** The metrics API has no throttling counters, so kray says "throttling likely" when CPU is at the limit.
- **It works without metrics.** If the cluster has no metrics API, kray still diagnoses from status, events, and logs, and says that usage isn't available.
