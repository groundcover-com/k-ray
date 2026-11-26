# Known limits

**kray can only read, and it hides secrets by pattern. Read these limits before you trust an answer or rely on the redaction.** Each one is a choice we made for safety or a gap we know about. The full list of what kray gives up is in [architecture §12](architecture.md#12-what-kray-gives-up-for-safety).

## The cluster identity is the real boundary

kray limits what your agent does through kray. It doesn't limit an agent that can run `kubectl` with your kubeconfig, or read your files.

- Create a read identity with `kray init`, and check it with `kray doctor`. Its token expires after 8 hours by default.
- Without `init`, kray uses your current kubeconfig context. On EKS, GKE, or AKS, client-go then runs the credential plugin of that context.

## Redaction can miss a secret

Redaction finds secrets by key name (`password`, `token`, `apikey` and more), by value shape (JWTs, PEM blocks, cloud and provider keys, bearer tokens, passwords in URLs and connection strings), and by randomness. A secret with a neutral name and a predictable value, such as `PIN=4711`, isn't found. Neither is a format that no rule knows.

**If you see a secret in kray output, report it privately.** Follow [SECURITY.md](../SECURITY.md) and don't open a public issue, because the report shows how a secret gets through. Include:

- the kray version (`kray version`) and the Kubernetes version;
- the exact tool call or command;
- the output, with the secret replaced by a fake value of the same shape;
- where the value lives in the cluster (an env value, a log line, a ConfigMap key, an annotation).

## A fault that no rule knows gets no cause

kray names a cause only when one of its fixed rules matches: OOM kills, pods that can't be scheduled, services with no ready endpoints, image pull failures, failing probes, crash loops, volume attach conflicts, and conditions that aren't healthy. For anything else, the `list` row has no cause, and `describe` shows what looks wrong with no likely cause. Your agent then gets trimmed data (the state, the warning events, the last log lines of a crashed container) and a `next:` line with a follow-up call, and it has to find the cause itself.

## kray sees the cluster now, not its history

kray reads what the API server holds at the moment of the call. It has no metrics store, no traces, and no request data.

- Usage comes from the metrics API: one snapshot, no history. A slow leak looks healthy until it's close to the limit.
- CPU throttling isn't measured. The metrics API has no throttling counters, so kray says "throttling likely" when CPU is at the limit.
- Events expire after about one hour by default. Older events are gone.
- Logs reach back to the previous container instance only (`previous=true`), up to 7 days with `since`, and up to 5,000 lines per pod with `tail`.

## Some things kray never does

- **No watch or follow.** There's no live tail. Your agent calls again with `since`.
- **No exec and no port-forward.** Your agent can't run a check inside a pod.
- **No Secret objects.** kray doesn't read them, list them, or check their names. A reference to a Secret is shown as a name and is never checked: a workload that refers to a Secret that doesn't exist isn't reported by kray. When the kubelet itself fails to start a container because of a missing Secret, kray repeats what the kubelet says.
- **Some names are unreadable.** An object or namespace named exactly `proxy`, `exec`, `attach`, `portforward` or `secrets` is blocked.
- **One cluster per process.** The context is fixed when kray starts.

## Speed and calls

**For one question, kray is about as fast as `kubectl`.** In the [agent benchmark](benchmarks.md), a single task took 11.0 seconds with `kubectl`, 10.6 seconds with the kray CLI, and 9.0 seconds with kray over MCP. A strong model needs about two calls with `kubectl`, and kray saves a fraction of one. The clear savings are tool tokens (62% fewer with the CLI) and calls in a long conversation.

**The MCP tools need more calls than the CLI.** In a conversation of ten questions, the MCP agent made 28.3 calls, the CLI agent 19.0, and the `kubectl` agent 26.0. An MCP agent can't chain commands, and it often starts with `list` instead of `describe`.

**A smaller model gains less.** With the smaller model in the benchmark, tool tokens fell by 43% and calls by 12%, and two answers of 54 were wrong (three with `kubectl`).

**A real cluster is different from the benchmark.** On a cluster with 1,000 pods, kray halved the calls of a long conversation but read more tool tokens than `kubectl` with filters. Those runs used private clusters and aren't in the repository. See [bench/agents](../bench/agents/README.md) for the method and its limits.

## A cause can be wrong

**A cause is a likely cause from fixed rules over one snapshot, not a certainty.** A rule can match the wrong evidence, or stay silent where it should speak. We try to make the rules say nothing when the evidence is history or belongs to another object, because a wrong cause is worse than none.

If a cause looks wrong:

1. Check the evidence that the answer quotes. Call `logs` (with `previous=true` for a crash), `events` for the object, or `get` for the spec.
2. Trust the evidence over the cause line.
3. Tell us: send the tool call, the output (with any secret replaced), and the real cause. A rule that states a wrong cause is a bug we want to fix.

The rules are listed in [docs/rules.md](rules.md).
