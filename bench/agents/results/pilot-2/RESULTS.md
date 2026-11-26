# Agent benchmark

Measured 2026-11-15. Model: `sonnet`. Runs: 24. Tool tokens are the bytes of tool output the model read, divided by 4.

## Totals per arm

| Arm | Correct | Tool calls (mean) | Tool tokens (mean) | Seconds (mean) | Cost (mean) | Runs that saw the secret |
|---|---:|---:|---:|---:|---:|---:|
| kubectl | 8 of 8 | 1.2 | 564 | 9 | $0.122 | 1 |
| cli | 8 of 8 | 1.9 | 899 | 10 | $0.084 | 0 |
| mcp | 8 of 8 | 2.1 | 492 | 9 | $0.107 | 0 |

## Per scenario

| Scenario | Arm | Correct | Tool calls | Tool tokens | Seconds |
|---|---|---:|---:|---:|---:|
| bad-image | kubectl | 1 of 1 | 1.0 | 627 | 8 |
| bad-image | cli | 1 of 1 | 1.0 | 524 | 9 |
| bad-image | mcp | 1 of 1 | 1.0 | 242 | 8 |
| crash-config | kubectl | 1 of 1 | 1.0 | 407 | 9 |
| crash-config | cli | 1 of 1 | 3.0 | 1415 | 12 |
| crash-config | mcp | 1 of 1 | 3.0 | 985 | 12 |
| missing-configmap | kubectl | 1 of 1 | 2.0 | 629 | 9 |
| missing-configmap | cli | 1 of 1 | 1.0 | 379 | 8 |
| missing-configmap | mcp | 1 of 1 | 1.0 | 210 | 9 |
| noisy-logs | kubectl | 1 of 1 | 1.0 | 448 | 10 |
| noisy-logs | cli | 1 of 1 | 2.0 | 1367 | 11 |
| noisy-logs | mcp | 1 of 1 | 3.0 | 1142 | 10 |
| oom | kubectl | 1 of 1 | 1.0 | 954 | 8 |
| oom | cli | 1 of 1 | 2.0 | 556 | 12 |
| oom | mcp | 1 of 1 | 2.0 | 346 | 9 |
| readiness | kubectl | 1 of 1 | 2.0 | 590 | 12 |
| readiness | cli | 1 of 1 | 2.0 | 779 | 11 |
| readiness | mcp | 1 of 1 | 3.0 | 351 | 13 |
| selector | kubectl | 1 of 1 | 1.0 | 379 | 9 |
| selector | cli | 1 of 1 | 3.0 | 1435 | 13 |
| selector | mcp | 1 of 1 | 2.0 | 294 | 8 |
| unschedulable | kubectl | 1 of 1 | 1.0 | 480 | 9 |
| unschedulable | cli | 1 of 1 | 1.0 | 735 | 8 |
| unschedulable | mcp | 1 of 1 | 2.0 | 370 | 7 |

## Wrong answers

None.
