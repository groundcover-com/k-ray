# Benchmarks

## Run it

You need Docker, `kubectl`, Go, and the `claude` CLI with an account. kind comes from `go run` when it isn't on your PATH.

1. Create the cluster `kray-bench` with metrics-server, and apply the faults of one set:

   ```bash
   make bench-up
   ```

2. Run the agents. The defaults are the blind set `heldout2`, the arms `kubectl`, `cli`, and `mcp`, 3 runs, and the model `sonnet`. That's 90 agent tasks at about two cents each.

   ```bash
   make bench-agents
   ```

3. Read `RESULTS.md` in `bench/agents/results/local-heldout2`. `results.json` has every run, and the `.jsonl` files hold the transcripts.

4. Delete the cluster:

   ```bash
   make bench-down
   ```

To run another set, give the same `BENCH_SET` to `bench-up` and `bench-agents`. For example, `BENCH_SET=heldout2-session` runs the ten questions as one conversation (about one dollar per run), and `BENCH_SET=hard/secrets` plants secrets of several kinds. `BENCH_RUNS`, `BENCH_MODEL`, `BENCH_ARMS`, and `BENCH_OUT` change the other defaults.

The output size test needs no model and costs nothing. It runs fixed commands with `kubectl` and with kray on the two hard sets, and compares bytes, commands, and canaries. Apply both sets, then run it:

```bash
make bench-up BENCH_SET=hard/tokens
```

```bash
make bench-up BENCH_SET=hard/secrets
```

```bash
make bench-hard
```

The result goes to `bench/HARD_RESULTS.md`. [`bench/agents/README.md`](../bench/agents/README.md) has every flag of the benchmark tool.

## Agent benchmark

**On 18 fault types from two blind fixture sets, an agent with the kray CLI reads 62% fewer tool tokens than an agent with `kubectl`, needs 19% fewer calls, is equally correct, and never sees a planted secret.** On the ten faults that nobody who changed kray ever saw, the saving is 52%. In a conversation of ten questions it reads 33% fewer tool tokens, needs 27% fewer calls, and takes 23% less time. The `kubectl` agent saw a planted token, password or key in 42% of its runs.

The fixtures come from two blind sets: each was written without a view of kray's code. The first set (8 faults) became a development set after its first run, and fixes were made for it. The second set (10 faults) stayed unseen by everyone who changed kray. The numbers are what kray controls: the output the agent reads, how often it has to ask, and what it must never see. All arms ran on the same night at one commit, before the first public release, on fresh fixtures, with a lean agent (a shell tool only, or only the MCP tools), on kind.

**18 single tasks, `sonnet`, three runs each (54 runs per arm):**

| Arm | Correct | Calls | Tool tokens | Seconds |
|---|---:|---:|---:|---:|
| `kubectl` | 54 of 54 | 2.0 | ~1,280 | 11.0 |
| kray, CLI | 54 of 54 | 1.6 | ~480 | 10.6 |
| kray, MCP | 54 of 54 | 2.6 | ~485 | 9.0 |

By set, tool tokens for `kubectl` and for the kray CLI: the unseen set ~1,120 and ~535 (52% fewer); the development set ~1,480 and ~420 (72% fewer).

**Ten reports as one long conversation, `sonnet`, three runs.** Every question is a new message in the same session, so every answer stays in the context:

| Arm | Questions correct | Calls | Tool tokens | Context at the end | Seconds |
|---|---:|---:|---:|---:|---:|
| `kubectl` | 30 of 30 | 26.0 | ~8,400 | ~28,300 | 133 |
| kray, CLI | 30 of 30 | 19.0 | ~5,660 | ~22,500 | 103 |
| kray, MCP | 30 of 30 | 28.3 | ~5,890 | ~25,200 | 115 |

**The same 18 single tasks with a smaller model, `haiku`:**

| Arm | Correct | Calls | Tool tokens | Seconds |
|---|---:|---:|---:|---:|
| `kubectl` | 51 of 54 | 2.8 | ~1,760 | 11.6 |
| kray, CLI | 52 of 54 | 2.5 | ~1,000 | 10.4 |
| kray, MCP | 52 of 54 | 3.8 | ~940 | 9.0 |

**Planted secrets, all runs of the round:** the `kubectl` agent saw one in 48 of 115 runs, and the kray agents in 0 of 230.

**What the round shows:**

- **The answers are smaller and the agent asks less.** With `sonnet`, tool tokens fall by 62% on single tasks and by 33% in the conversation; calls fall by 19% and 27%; the conversation is 23% shorter in time.
- **The secrets are the largest difference.** The faults have nothing to do with secrets. The agent meets them in env values, arguments, log lines and annotations while it debugs.
- **Speed for one question is about equal.** A strong model with `kubectl` needs two calls; kray saves a fraction of one.
- **rtk made no difference for Kubernetes in the round before this one.** `kubectl` through rtk 1.2.0 read ~1,210 tool tokens against ~1,180 for plain `kubectl` that night, and saw a planted secret in 46 of 115 runs. It passes `describe`, logs and events through, and it doesn't redact. It wasn't run again in this round.

**Where kray is weaker:**

- **A smaller model gains less.** With `haiku`, tool tokens fall by 43% and calls by 12%, and two answers of 54 were wrong (`kubectl`: three).
- **The MCP surface needs more calls than the CLI:** 28.3 against 19.0 in the conversation. An MCP agent can't chain commands, and it starts more tasks with a list.
- **The benchmark could not see part of a secret.** Every planted secret is one token. A code review found four paths where part of a secret of several words or lines could reach the output. They're fixed, and the canary harness now plants such secrets, but this benchmark still plants single tokens.
- **A fix can cost an answer.** An earlier version showed two workloads of the same name, one of them without its cause, and `haiku` picked the wrong one in three runs. The first fix made `sonnet` stop one call early and miss a detail. Both were found by the blind set and fixed before this round.
- **It's one benchmark.** Two models, three runs per task, a keyword check for grading, and small fixtures on kind. Fresh faults have more recent events, so `kubectl` read more in this round than in the one before (~1,280 tool tokens against ~1,180): compare arms only from the same night. See [bench/agents](../bench/agents/README.md) for the method, the sets, how to run it, and the limits.

## Tokens and calls saved

**On six debugging tasks, kray needs 41% fewer calls and 32% fewer tokens than `kubectl`, and it shows none of the planted secrets.** Every tool result goes into your agent's context window, and every call costs a model turn that reads the whole context again. Fewer calls is the largest saving.

| | `kubectl` | `kubectl` through rtk 1.2.0 | kray |
|---|---:|---:|---:|
| Calls | 22 | 22 | 13 |
| Tokens read | ~11,750 | ~16,650 | ~7,950 |
| Planted secrets shown | 5 of 13 | 5 of 13 | 0 |

`make bench-hard` runs this test with no model. For each task it runs a fixed list of commands on a kind cluster with 100 pods, three planted faults in a noisy shop, and three faults that sit next to planted secrets. The `kubectl` lists use the filters an engineer writes (`grep`, `--tail`, selectors). The kray lists start from `kray get pods -A`. Tokens are estimated as bytes / 4. Raw data: [bench/HARD_RESULTS.md](../bench/HARD_RESULTS.md).

**On a real cluster, single commands shrink by 63% to 96%.** These were measured with read commands on a production cluster with 40 pods and 2 nodes:

| Command | `kubectl` tokens | kray tokens | Saved |
|---|---:|---:|---:|
| `get events -A` | ~9,070 | ~360 | 96% |
| `describe pod` | ~3,130 | ~960 | 69% |
| `logs deploy/NAME --tail=500` | ~2,850 | ~915 | 68% |
| `get pods -A` | ~1,225 | ~400 | 67% |
| `get deploy -A` | ~545 | ~205 | 63% |
| `describe deployment` | ~1,650 | ~615 | 63% |
| `get pod NAME -o yaml` | ~5,300 | ~3,990 | 25% |

**How kray keeps answers small:**

- **The first call shows the cause, on the row.** Every row of `get pods -A` carries its cause, so a `grep` for a name keeps it. `describe NAME` finds the object in any namespace, and `describe service` names a port that matches no container.
- **A row is only for a problem.** Healthy objects are counted by workload, one line for each namespace.
- **One call reads a whole workload.** `logs` merges the lines of all pods into groups and says which pods have each group. A stack trace is one group with its cause lines.
- **Each fact appears once.** Repeated names, UIDs, and image references go away, and normal events are counted, not listed, when warnings exist.
- **Every result fits a size budget** (16 KiB, about 4,000 tokens, by default). A cut says what's missing and how to get more.

**What the numbers don't show:**

- **The command lists are written by hand.** An agent chooses its own commands. The [agent benchmark](#) is the real test.
- **The output was tuned on these six tasks.** New faults that kray hasn't seen are the next round.
- **`get ... -o yaml` saves little.** kray still returns most of the object there.
