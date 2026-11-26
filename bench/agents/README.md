# Agent benchmark

Commit IDs in this file, and in the names of the `results/final-*` folders, come from the development history before the public repository. Those commits are not in this repository.

**This benchmark asks one question: does an AI agent find the root cause of a Kubernetes fault better with kray than with `kubectl`?** It gives the same task to the same model with different tools, on a cluster with known faults, and measures what each agent needs.

It's a development tool. It runs the `claude` CLI in headless mode, so it needs that CLI and an account. The kray binary itself makes no LLM calls.

## What it measures

| Metric | Meaning |
|---|---|
| Correct | The answer names the known root cause. A scenario lists the facts the answer must contain. |
| Tool calls | How many times the agent called a tool. |
| Tool tokens | The bytes of tool output the model read, divided by 4. |
| Seconds | Wall time of the whole agent run. |
| Cost | What the run cost, as the CLI reports it. |
| Saw the secret | Whether a planted secret value reached the model. |

## Arms

| Arm | The agent's tool |
|---|---|
| `kubectl` | The `kubectl` CLI. The harness allows only read verbs (`get`, `describe`, `logs`, `top`, `events`). |
| `cli` | kray as a CLI, through the shell. |
| `mcp` | kray as an MCP server. |

The shell arms can also use text filters such as `grep`, `head`, and `tail`. The task text is the same for every arm, except the line that names the tool.

## Scenarios

Each scenario is one namespace with one fault, in `scenarios/<name>/`. `manifest.yaml` creates the fault. `truth.json` holds the symptom the agent gets, the root cause, and the facts a correct answer must contain.

| Scenario | Fault |
|---|---|
| `oom` | A container is OOM killed at its 32Mi limit. |
| `bad-image` | An image tag that doesn't exist. |
| `selector` | A service selector that matches no pod. |
| `unschedulable` | A pod that requests more memory than any node has. |
| `readiness` | A readiness probe that requests a path the app doesn't serve. |
| `crash-config` | A container that exits because an environment variable is missing. |
| `missing-configmap` | A pod that refers to a ConfigMap that doesn't exist. |
| `noisy-logs` | A crash whose one error line follows 3,000 info lines. The pod also holds a planted secret. |

## Run it

You need Docker, `kubectl`, Go, and the `claude` CLI.

1. Start a kind cluster with metrics-server:

   ```bash
   KIND_CLUSTER_NAME=kray-bench ./scripts/kind.sh up
   KIND_CLUSTER_NAME=kray-bench ./scripts/kind.sh metrics-server
   ```

2. Create the faults and wait until each one shows:

   ```bash
   go run ./bench/agents -apply
   ```

3. Run the agents. This example runs every scenario once per arm:

   ```bash
   go run ./bench/agents -runs 1 -model sonnet -out bench/agents/results/my-run
   ```

4. Read `RESULTS.md` in the output directory. `results.json` has every run, and the `.jsonl` files hold the full transcripts.

Useful flags: `-only oom,selector` runs some scenarios, `-arms kubectl,mcp` runs some arms, and `-runs 3` repeats each cell.

## The blind benchmark

**This is the round to trust: two blind fixture sets, three arms the same night at one commit, two models.** Only `heldout2` stayed unseen by everyone who changed the tool; on it the CLI read 52% fewer tool tokens than `kubectl` (533 against 1,119), and on `heldout`, the development set, 72% fewer (418 against 1,481). All rows are from commit `3e0d5f6` on fresh fixtures (`results/final-3e0d5f6/`). The round before it, which has the rtk arm, is below.

**How it stays blind.** `heldout/` (8 faults) and `heldout2/` (10 faults) were each written by someone who did not read the tool's code and did not run the tool. The tool was then changed by someone who did not read `heldout2/`. `heldout/` was looked at after its first run, so it counts as a development set; `heldout2/` stayed unseen.

**What is measured.** The headline numbers are the ones the tool controls: the tool output the agent reads (tool tokens), the calls, and the planted secrets that reach the agent. Total input tokens, seconds and cost are in the result files too, but they depend on the agent around the tool.

**The agent.** `-lean` gives the agent no user settings, plugins or skills, and only the shell tool (or only the MCP tools). Without it a run inherits the settings of the person who runs it, which on one machine added 66,000 tokens to every turn. The `kubectl` and rtk arms run `kubectl` through a shim that lets only read commands through. The kray CLI arm gets one line of usage in its task; the `kubectl` arms get none, because the model knows `kubectl`.

**Single tasks and conversations.** A set directory holds single tasks. `heldout-session/` and `heldout2-session/` ask the same reports as one conversation: each question is a new prompt in the same session, graded on its own, so the context grows as in real work.

**Run it:**

```bash
KIND_CLUSTER_NAME=kray-bench ./scripts/kind.sh up
KIND_CLUSTER_NAME=kray-bench ./scripts/kind.sh metrics-server
go run ./bench/agents -apply -scenarios bench/agents/heldout
go run ./bench/agents -apply -scenarios bench/agents/heldout2
go run ./bench/agents -lean -runs 3 -model sonnet -scenarios bench/agents/heldout2 -arms kubectl,cli,mcp -out bench/agents/results/my-run
go run ./bench/agents -lean -runs 3 -model sonnet -scenarios bench/agents/heldout2-session -arms kubectl,cli -out bench/agents/results/my-conversation
```

The rtk arm needs `rtk` on the PATH. A conversation costs about one dollar per run with `sonnet`; a single task about two cents.

**Results, `sonnet`, 18 single tasks, 54 runs per arm:**

| Arm | Correct | Calls | Tool tokens | Seconds |
|---|---:|---:|---:|---:|
| `kubectl` | 54 of 54 | 2.0 | 1,280 | 11.0 |
| kray CLI | 54 of 54 | 1.6 | 482 | 10.6 |
| kray MCP | 54 of 54 | 2.6 | 486 | 9.0 |

**Per set (calls; tool tokens), `sonnet`:** `heldout2`: `kubectl` 2.0; 1,119. CLI 1.7; 533. MCP 2.2; 462. `heldout`: `kubectl` 2.0; 1,481. CLI 1.5; 418. MCP 3.2; 516.

**Conversations, `sonnet` (questions correct; calls; tool tokens; seconds):**

| Set | `kubectl` | kray CLI | kray MCP |
|---|---|---|---|
| `heldout2-session`, 10 questions, 3 runs | 30/30; 26.0; 8,404; 133 | 30/30; 19.0; 5,663; 103 | 30/30; 28.3; 5,890; 115 |
| `heldout-session`, 8 questions, 2 runs | 16/16; 18.5; 7,253; 120 | 16/16; 15.5; 5,742; 89 | 16/16; 26.5; 6,025; 99 |

**`haiku`:** single tasks (54 runs per arm): `kubectl` 51 of 54, 2.8 calls, 1,757 tool tokens; kray CLI 52 of 54, 2.5, 1,001; kray MCP 52 of 54, 3.8, 938. Conversation on `heldout2-session` (2 runs): `kubectl` 19/20, 31.5 calls, 13,581 tool tokens; kray CLI 20/20, 26.0, 8,792; kray MCP 20/20, 36.0, 6,708.

**Planted secrets seen:** `kubectl` 48 of 115 runs, kray CLI 0 of 115, kray MCP 0 of 115.

**The round before, with rtk.** At commit `a0612a8` for `kubectl` and rtk, and `81f89ce` for kray (`results/final-a0612a8/`, `results/final-81f89ce/`), on fixtures that were some hours old. `sonnet`, 54 single runs per arm: `kubectl` 54 of 54, 2.2 calls, 1,182 tool tokens; rtk 1.2.0 54 of 54, 2.3, 1,206; kray CLI 54 of 54, 1.7, 488; kray MCP 54 of 54, 2.6, 504. Conversation on `heldout2-session`: `kubectl` 25.7 calls, 6,536 tool tokens; rtk 29.3, 8,582; kray CLI 22.3, 5,104; kray MCP 31.7, 6,153. Secrets: `kubectl` 45 of 115 runs, rtk 46 of 115, kray 0 of 230. rtk was not run again.

**Known weak points of this round:**

- **Every planted secret is one token.** A code review found four paths where part of a secret of several words or lines could reach the output (a cut before redaction, a key block over several log lines, a flag value with spaces, free text of Events before the fence). This benchmark could not see them. They are fixed, and the canary harness (`test/harness`) now plants such secrets; the fixture sets here still plant single tokens.
- **The age of the fixtures changes the numbers.** Fresh faults have more recent events. `kubectl` read 1,280 tool tokens in this round and 1,182 in the round before; the kray CLI read 482 and 488. Compare arms only from the same night.
- **The two sets share one name.** Both have a Deployment `invoice-api`, in different namespaces, with different faults. An earlier commit showed both and only one had its cause; `haiku` picked the wrong one in all three runs. The first fix made `sonnet` stop one call early and miss a detail. Both were fixed before this round.
- **Run-to-run noise is large.** Rounds of the same task set gave 424 to 533 tool tokens for the CLI and 975 to 1,119 for `kubectl`. One change that was tried after this round (other words in two MCP tool descriptions) moved the MCP calls by -0.3, +0.2 and +5.4 on three sets with no change in which tool the agent called first; it was dropped as noise.
- **The MCP surface needs more calls than the CLI.** The MCP agent started 20 of 54 single tasks with `list` or `logs`, not with `describe`.
- **A real cluster is different.** On a cluster with 1,000 pods the tool halved the calls of a long conversation and read more tool tokens than `kubectl` with filters. Those runs used private clusters and are not in the repository.

## Earlier rounds on small faults

**On eight small faults, kray now equals `kubectl` on speed and cost, and no kray run exposed the planted secret.** Each pilot is 24 runs: 8 scenarios, 3 arms, 1 run per cell, model `sonnet`, on kind with Kubernetes 1.37. Every arm named the correct root cause in every run. Between pilots, kray changed; `kubectl` and the scenarios did not.

| Pilot | What changed in kray | Arm | Tool calls | Tool tokens | Seconds | Saw the secret |
|---|---|---|---:|---:|---:|---:|
| 1 | Baseline | `kubectl` | 1.2 | 678 | 9 | 1 of 8 |
| | | `cli` | 3.5 | 1,272 | 14 | 0 of 8 |
| | | `mcp` | 3.5 | 691 | 12 | 0 of 8 |
| 2 | Causes in `list`; the last crash in a workload's `describe`; command and environment names in the summary | `kubectl` | 1.2 | 564 | 9 | 1 of 8 |
| | | `cli` | 1.9 | 899 | 10 | 0 of 8 |
| | | `mcp` | 2.1 | 492 | 9 | 0 of 8 |
| 3 | No `next:` line after a cause; service health in `list`; `endpoints` accepted | `kubectl` | 1.5 | 494 | 10 | 1 of 8 |
| | | `cli` | 1.4 | 695 | 11 | 0 of 8 |
| | | `mcp` | 2.0 | 411 | 9 | 0 of 8 |

Values are means per run. The full tables are in `results/pilot-1`, `pilot-2`, and `pilot-3`.

**What the pilots show:**

- **kray started behind and caught up.** In pilot 1, a `kubectl` agent chained `get`, `describe`, and `logs` in one shell call and finished in 9 seconds. kray needed 3.5 calls. The fix was to put the likely cause in the first call.
- **The three arms are now equal within noise** on calls, seconds, and cost. One run per cell can't show a difference of one second.
- **kray over MCP reads the fewest tokens** in pilots 2 and 3.
- **Safety is the clear difference.** In every pilot, the `kubectl` agent in `noisy-logs` printed the pod's environment and saw the planted API key. No kray run saw it.

**What the pilots don't show:** that kray is faster than `kubectl`. These faults are small and clean, and a strong model solves each one in one or two calls with either tool. The next round needs harder cases: large noisy namespaces, faults with no namespace hint, a weaker model, and three or more runs per cell.

Two changes to the harness after pilot 1 apply to pilots 2 and 3: the `cli` arm gets kray's usage in one line (the model knows `kubectl` but not kray), and tool lookup is off so that no arm spends a call to find its tools.

## Hard benchmarks

**The small scenarios ask whether an agent finds the root cause. The hard benchmarks ask whether it finds the cause while reading few tokens and seeing no secret.** Correctness is a gate; tokens and leaked secrets are the score. Each hard set is one shared fixture with several tasks, in `hard/<set>/`. The task text names no namespace.

| Set | Fixture | Tasks |
|---|---|---|
| `hard/tokens` | "shop": four `shop-*` namespaces, about 70 pods, a CronJob and old completed Jobs, pods with an old restart, several workloads that log thousands of lines, and hundreds of normal events. | `stack-traces`, `bad-release`, `wrong-port` |
| `hard/secrets` | "bank": four `bank-*` namespaces, 13 distinct fake secrets planted next to each fault, and two real `Secret` objects. | `db-password`, `config-typo`, `upstream-403` |

| Task | Fault |
|---|---|
| `stack-traces` | A StatefulSet with 20 replicas. Three of them (3, 10, 17) print the same 40-line Java stack trace more than 500 times among INFO lines. All replicas print 10,000 INFO lines. The trace names a payment gateway timeout. |
| `bad-release` | A Deployment rolls to revision 2, which changes the readiness probe path to `/api/health`. The app serves `/healthz`. The new pod never becomes ready and the old ReplicaSet keeps serving. Revision 2 also changes an annotation and an environment variable that do not matter. |
| `wrong-port` | The `edge` service logs 502 errors for `/api/stock`. The cause is in the `inventory` Service in another namespace: its `targetPort` is `http-api`, but the container port is named `http`. The EndpointSlice has endpoints and no ports. |
| `db-password` | The `ledger` pod exits because the database rejects the password. The log line prints the full connection string, and the pod spec holds the password as a plain env value. |
| `config-typo` | A ConfigMap holds an app config with one wrong hostname, an embedded `.env` file with a basic-auth URL, and a PEM private key. The app logs a DNS error for the wrong host. |
| `upstream-403` | The gateway's JSON request logs hold bearer tokens, signed URLs and a nested `client_secret`. One path, `/v1/ledger/export`, gets HTTP 403. |

Files in a set directory:

- `set.json` lists the namespaces and the apply steps. `bad-release` needs two steps: the harness applies revision 1, waits until it is ready, then applies revision 2.
- `canaries.json` lists each planted secret: id, kind, value, and where it is planted. Every value is fake and, where the format allows, contains `KrayCanary`. An AWS access key id cannot hold mixed case, so it contains `KRAYCANARY`.
- `keep.json` lists values that look secret-like but are not, and that a debugger needs: git SHAs, an image digest, UUID request ids, hostnames, pod names.
- `<task>/truth.json` is the symptom, the root cause, the `must` groups, and how to know the fault shows. `<task>/questions.json` is for the free test.

### Apply, run and delete

```bash
go run ./bench/agents -apply  -scenarios bench/agents/hard/tokens
go run ./bench/agents -apply  -scenarios bench/agents/hard/secrets
go run ./bench/agents -runs 1 -model sonnet -scenarios bench/agents/hard/tokens  -out bench/agents/results/hard-tokens
go run ./bench/agents -delete -scenarios bench/agents/hard/tokens
```

`-apply` is safe to run again. It waits until each fault shows, and it waits for the rollouts in `set.json` first. `-delete` removes the namespaces of the set and does not wait. The harness uses the kubeconfig in `-kubeconfig`; check that its current context is the benchmark cluster.

The fixtures need memory. Each pod asks for at most 5m CPU and 8Mi of memory, and every container makes its log with one `awk` process and then sleeps. Pod starts are staggered. The cluster needs room for about 80 more pods (under the kubelet limit of 110) and a Docker VM with several GiB of memory.

For each run, the report also shows what the CLI says about real usage: input tokens (summed over all turns, cache reads and writes included), output tokens, turns, and the peak context of one turn. In a set with `canaries.json`, the harness counts the distinct canaries in the transcript, each checked plain, in base64 and URL-encoded, and lists their ids. For the small scenarios it checks the one planted key as before.

### Free size test

`make bench-hard` runs no agent and no LLM. For each hard task it runs the commands in `questions.json` with read-only `kubectl`, and the commands with the kray CLI. It measures output bytes (tokens are bytes divided by 4), the number of commands, whether every `must` group matches the output, how many canaries appear, and how many keep values that `kubectl` showed are missing from the kray output. The result goes to `bench/HARD_RESULTS.md`. Use it after each change to kray output.

Rules for the question files:

- The `kubectl` list is what a competent engineer runs: label selectors, `--tail`, and `grep` pipes.
- The `kray` list uses only the six commands of the CLI. Today `kray logs` takes one pod, so the 20 replicas need a shell loop.
- An optional `variants` map adds more rows, for example a `kubectl` run with no `grep`, or a kray run with a `grep` triage step.
- A `must` group is checked against raw tool output, so write it with words that appear in the evidence.

Baseline, measured on the fixtures above, with kray at commit `5ceab3d` plus the working tree (tokens are bytes divided by 4):

**Set `tokens`**


| Task | Side | Commands | Bytes | ~Tokens | Answer in output | Canaries | Keep missing |
|---|---|---:|---:|---:|---|---:|---|
| bad-release | kubectl | 4 | 6305 | 1576 | yes | 0 | - |
| bad-release | kray | 3 | 6814 | 1703 | yes | 0 | 0 of 2 |
| stack-traces | kubectl | 3 | 3215 | 803 | yes | 0 | - |
| stack-traces | kray | 2 | 44024 | 11006 | yes | 0 | 1 of 4 |
| stack-traces | kray-triage | 3 | 8088 | 2022 | yes | 0 | 1 of 4 |
| stack-traces | kubectl-no-grep | 2 | 282566 | 70641 | yes | 0 | 0 of 4 |
| wrong-port | kubectl | 6 | 11438 | 2859 | yes | 0 | - |
| wrong-port | kray | 6 | 7102 | 1775 | yes | 0 | 0 of 2 |

| Task | kubectl ~tokens | kray ~tokens | kubectl / kray |
|---|---:|---:|---:|
| bad-release | 1576 | 1703 | 0.9x |
| stack-traces | 803 | 11006 | 0.1x |
| wrong-port | 2859 | 1775 | 1.6x |

**Set `secrets`**


| Task | Side | Commands | Bytes | ~Tokens | Answer in output | Canaries | Keep missing |
|---|---|---:|---:|---:|---|---:|---|
| config-typo | kubectl | 4 | 10546 | 2636 | yes | 3 | - |
| config-typo | kray | 4 | 18284 | 4571 | yes | 0 | 0 of 4 |
| db-password | kubectl | 3 | 6168 | 1542 | yes | 1 | - |
| db-password | kray | 3 | 17756 | 4439 | yes | 0 | 0 of 6 |
| upstream-403 | kubectl | 2 | 5620 | 1405 | yes | 1 | - |
| upstream-403 | kray | 2 | 3183 | 795 | yes | 0 | 0 of 4 |

| Task | kubectl ~tokens | kray ~tokens | kubectl / kray |
|---|---:|---:|---:|
| config-typo | 2636 | 4571 | 0.6x |
| db-password | 1542 | 4439 | 0.3x |
| upstream-403 | 1405 | 795 | 1.8x |


How to read it: **the baseline is the score to beat, not a win.** On `stack-traces`, `kubectl` with a `grep` pipeline reads far less than kray, because kray cannot read the logs of 20 pods in one call. On `db-password` and `config-typo`, kray reads more tokens than `kubectl` but shows no canary. `kubectl` shows between one and three canaries in each secrets task. The full table, with details per side, is in `bench/HARD_RESULTS.md`.

## Limits

- **The grading is a keyword check.** It can pass a vague answer or fail a correct one that uses other words. Read the answers in `results.json` before you trust a close result.
- **One run per cell is not statistics.** Agent runs vary. Use `-runs 3` or more for a claim.
- **The scenarios are small and clean.** Each namespace has one fault and no noise. Real clusters are larger, and the faults there are less direct.
- **The model matters.** A strong model writes good `kubectl` pipelines. A weaker model may not. Results from one model don't transfer to another.
- **Transcripts can hold cluster data.** The `.jsonl` files are not committed (see `.gitignore`).
