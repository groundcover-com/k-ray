# Agent benchmark

Measured 2026-11-15. Model: `sonnet`. Runs: 24. Tool tokens are the bytes of tool output the model read, divided by 4.

## Totals per arm

| Arm | Correct | Tool calls (mean) | Tool tokens (mean) | Seconds (mean) | Cost (mean) | Runs that saw the secret |
|---|---:|---:|---:|---:|---:|---:|
| kubectl | 8 of 8 | 1.2 | 678 | 9 | $0.071 | 1 |
| cli | 8 of 8 | 3.5 | 1272 | 14 | $0.086 | 0 |
| mcp | 8 of 8 | 3.5 | 691 | 12 | $0.094 | 0 |

## Per scenario

| Scenario | Arm | Correct | Tool calls | Tool tokens | Seconds |
|---|---|---:|---:|---:|---:|
| bad-image | kubectl | 1 of 1 | 1.0 | 405 | 8 |
| bad-image | cli | 1 of 1 | 2.0 | 753 | 11 |
| bad-image | mcp | 1 of 1 | 2.0 | 193 | 9 |
| crash-config | kubectl | 1 of 1 | 1.0 | 401 | 8 |
| crash-config | cli | 1 of 1 | 5.0 | 1554 | 13 |
| crash-config | mcp | 1 of 1 | 5.0 | 1303 | 16 |
| missing-configmap | kubectl | 1 of 1 | 2.0 | 621 | 12 |
| missing-configmap | cli | 1 of 1 | 3.0 | 641 | 12 |
| missing-configmap | mcp | 1 of 1 | 3.0 | 241 | 12 |
| noisy-logs | kubectl | 1 of 1 | 1.0 | 922 | 9 |
| noisy-logs | cli | 1 of 1 | 6.0 | 3046 | 24 |
| noisy-logs | mcp | 1 of 1 | 5.0 | 1348 | 17 |
| oom | kubectl | 1 of 1 | 1.0 | 1312 | 8 |
| oom | cli | 1 of 1 | 3.0 | 1406 | 13 |
| oom | mcp | 1 of 1 | 4.0 | 868 | 12 |
| readiness | kubectl | 1 of 1 | 2.0 | 1035 | 10 |
| readiness | cli | 1 of 1 | 4.0 | 1233 | 16 |
| readiness | mcp | 1 of 1 | 5.0 | 1124 | 12 |
| selector | kubectl | 1 of 1 | 1.0 | 249 | 7 |
| selector | cli | 1 of 1 | 3.0 | 834 | 14 |
| selector | mcp | 1 of 1 | 2.0 | 165 | 8 |
| unschedulable | kubectl | 1 of 1 | 1.0 | 481 | 8 |
| unschedulable | cli | 1 of 1 | 2.0 | 706 | 9 |
| unschedulable | mcp | 1 of 1 | 2.0 | 288 | 8 |

## Wrong answers

None.
