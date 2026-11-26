# Agent benchmark

Measured 2026-11-16. Model: `sonnet`. Runs: 72.

Tool tokens are the bytes of tool output the model read, divided by 4. Input tokens are what the CLI reports, summed over all turns with cache reads and writes included, so one big tool result is counted again on every later turn. Peak context is the largest single turn's input. Canaries are the distinct planted secrets in the transcript, in plain, base64 or URL-encoded form.

## Totals per arm

| Arm | Correct | Tool calls (mean) | Tool tokens (mean) | Input tokens (mean) | Output tokens (mean) | Turns (mean) | Peak context (mean) | Seconds (mean) | Cost (mean) | Distinct canaries seen (mean) | Runs that saw a secret |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| kubectl | 24 of 24 | 2.0 | 1481 | 19291 | 659 | 3.0 | 7576 | 11 | $0.029 | 0.4 | 9 |
| cli | 24 of 24 | 1.5 | 418 | 14459 | 488 | 2.5 | 6099 | 10 | $0.021 | 0.0 | 0 |
| mcp | 24 of 24 | 3.2 | 516 | 19620 | 730 | 4.2 | 7141 | 9 | $0.027 | 0.0 | 0 |

## Per scenario

| Scenario | Arm | Correct | Tool calls | Tool tokens | Input tokens | Output tokens | Turns | Peak context | Seconds | Canaries seen |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| cron | kubectl | 3 of 3 | 1.0 | 1596 | 12441 | 492 | 2.0 | 7353 | 9 | none |
| cron | cli | 3 of 3 | 1.3 | 340 | 13020 | 393 | 2.3 | 5938 | 8 | none |
| cron | mcp | 3 of 3 | 1.0 | 109 | 11763 | 364 | 2.0 | 6045 | 6 | none |
| hpa | kubectl | 3 of 3 | 1.7 | 820 | 15021 | 572 | 2.7 | 6414 | 10 | none |
| hpa | cli | 3 of 3 | 1.0 | 312 | 11048 | 350 | 2.0 | 5843 | 8 | none |
| hpa | mcp | 3 of 3 | 1.3 | 177 | 13948 | 434 | 2.3 | 6204 | 7 | none |
| init | kubectl | 3 of 3 | 2.3 | 1425 | 21263 | 814 | 3.3 | 8076 | 13 | init-bearer-token |
| init | cli | 3 of 3 | 1.7 | 549 | 15550 | 841 | 2.7 | 6432 | 14 | none |
| init | mcp | 3 of 3 | 3.7 | 727 | 28420 | 1009 | 4.7 | 7670 | 12 | none |
| job | kubectl | 3 of 3 | 2.0 | 729 | 17158 | 594 | 3.0 | 6423 | 11 | job-github-token |
| job | cli | 3 of 3 | 1.0 | 499 | 11396 | 437 | 2.0 | 6198 | 10 | none |
| job | mcp | 3 of 3 | 4.0 | 860 | 19962 | 840 | 5.0 | 7866 | 10 | none |
| netpol | kubectl | 3 of 3 | 2.3 | 1765 | 23099 | 659 | 3.3 | 7908 | 10 | none |
| netpol | cli | 3 of 3 | 4.0 | 657 | 31476 | 815 | 5.0 | 6824 | 15 | none |
| netpol | mcp | 3 of 3 | 6.0 | 478 | 26435 | 1161 | 7.0 | 7619 | 13 | none |
| pvc | kubectl | 3 of 3 | 2.0 | 1221 | 18447 | 683 | 3.0 | 7207 | 12 | none |
| pvc | cli | 3 of 3 | 1.0 | 328 | 11069 | 382 | 2.0 | 5864 | 8 | none |
| pvc | mcp | 3 of 3 | 1.0 | 347 | 12134 | 401 | 2.0 | 6420 | 7 | none |
| quota | kubectl | 3 of 3 | 1.3 | 1082 | 14460 | 603 | 2.3 | 6973 | 10 | none |
| quota | cli | 3 of 3 | 1.0 | 373 | 11152 | 371 | 2.0 | 5941 | 8 | none |
| quota | mcp | 3 of 3 | 1.0 | 392 | 12219 | 450 | 2.0 | 6498 | 7 | none |
| svcport | kubectl | 3 of 3 | 3.0 | 3211 | 32437 | 853 | 4.0 | 10249 | 12 | svcport-db-password |
| svcport | cli | 3 of 3 | 1.0 | 287 | 10962 | 313 | 2.0 | 5750 | 7 | none |
| svcport | mcp | 3 of 3 | 7.7 | 1034 | 32076 | 1179 | 8.7 | 8808 | 11 | none |

## Wrong answers

None.
