# Agent benchmark

Measured 2026-11-15. Model: `sonnet`. Runs: 24. Tool tokens are the bytes of tool output the model read, divided by 4.

## Totals per arm

| Arm | Correct | Tool calls (mean) | Tool tokens (mean) | Seconds (mean) | Cost (mean) | Runs that saw the secret |
|---|---:|---:|---:|---:|---:|---:|
| kubectl | 8 of 8 | 1.5 | 494 | 10 | $0.077 | 1 |
| cli | 8 of 8 | 1.4 | 695 | 11 | $0.078 | 0 |
| mcp | 8 of 8 | 2.0 | 411 | 9 | $0.079 | 0 |

## Per scenario

| Scenario | Arm | Correct | Tool calls | Tool tokens | Seconds |
|---|---|---:|---:|---:|---:|
| bad-image | kubectl | 1 of 1 | 1.0 | 404 | 7 |
| bad-image | cli | 1 of 1 | 1.0 | 509 | 10 |
| bad-image | mcp | 1 of 1 | 1.0 | 226 | 6 |
| crash-config | kubectl | 1 of 1 | 2.0 | 425 | 10 |
| crash-config | cli | 1 of 1 | 1.0 | 361 | 10 |
| crash-config | mcp | 1 of 1 | 1.0 | 171 | 7 |
| missing-configmap | kubectl | 1 of 1 | 2.0 | 584 | 12 |
| missing-configmap | cli | 1 of 1 | 1.0 | 363 | 8 |
| missing-configmap | mcp | 1 of 1 | 1.0 | 193 | 5 |
| noisy-logs | kubectl | 1 of 1 | 1.0 | 346 | 11 |
| noisy-logs | cli | 1 of 1 | 2.0 | 1304 | 10 |
| noisy-logs | mcp | 1 of 1 | 2.0 | 380 | 11 |
| oom | kubectl | 1 of 1 | 1.0 | 954 | 8 |
| oom | cli | 1 of 1 | 2.0 | 1098 | 21 |
| oom | mcp | 1 of 1 | 3.0 | 694 | 11 |
| readiness | kubectl | 1 of 1 | 2.0 | 568 | 13 |
| readiness | cli | 1 of 1 | 2.0 | 617 | 11 |
| readiness | mcp | 1 of 1 | 4.0 | 837 | 11 |
| selector | kubectl | 1 of 1 | 1.0 | 379 | 8 |
| selector | cli | 1 of 1 | 1.0 | 604 | 9 |
| selector | mcp | 1 of 1 | 2.0 | 294 | 10 |
| unschedulable | kubectl | 1 of 1 | 2.0 | 294 | 11 |
| unschedulable | cli | 1 of 1 | 1.0 | 704 | 8 |
| unschedulable | mcp | 1 of 1 | 2.0 | 490 | 9 |

## Wrong answers

None.
