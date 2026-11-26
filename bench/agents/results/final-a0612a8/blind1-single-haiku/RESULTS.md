# Agent benchmark

Measured 2026-11-16. Model: `haiku`. Runs: 96.

Tool tokens are the bytes of tool output the model read, divided by 4. Input tokens are what the CLI reports, summed over all turns with cache reads and writes included, so one big tool result is counted again on every later turn. Peak context is the largest single turn's input. Canaries are the distinct planted secrets in the transcript, in plain, base64 or URL-encoded form.

## Totals per arm

| Arm | Correct | Tool calls (mean) | Tool tokens (mean) | Input tokens (mean) | Output tokens (mean) | Turns (mean) | Peak context (mean) | Seconds (mean) | Cost (mean) | Distinct canaries seen (mean) | Runs that saw a secret |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| kubectl | 24 of 24 | 2.8 | 2087 | 28517 | 1306 | 3.8 | 9891 | 11 | $0.002 | 0.3 | 7 |
| rtk | 23 of 24 | 2.6 | 1823 | 26181 | 1192 | 3.6 | 9425 | 11 | $0.002 | 0.5 | 11 |
| cli | 21 of 24 | 2.3 | 1051 | 22756 | 1180 | 3.3 | 8471 | 11 | $0.002 | 0.0 | 0 |
| mcp | 22 of 24 | 4.3 | 1011 | 24670 | 1110 | 5.3 | 9038 | 9 | $0.002 | 0.0 | 0 |

## Per scenario

| Scenario | Arm | Correct | Tool calls | Tool tokens | Input tokens | Output tokens | Turns | Peak context | Seconds | Canaries seen |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| cron | kubectl | 3 of 3 | 1.7 | 441 | 17231 | 817 | 2.7 | 7103 | 8 | none |
| cron | rtk | 3 of 3 | 1.3 | 974 | 15910 | 722 | 2.3 | 7904 | 8 | none |
| cron | cli | 3 of 3 | 2.0 | 453 | 20031 | 1027 | 3.0 | 7405 | 10 | none |
| cron | mcp | 3 of 3 | 2.0 | 167 | 13715 | 651 | 3.0 | 7176 | 6 | none |
| hpa | kubectl | 3 of 3 | 2.0 | 781 | 19936 | 961 | 3.0 | 7582 | 9 | none |
| hpa | rtk | 3 of 3 | 2.0 | 1024 | 20160 | 1101 | 3.0 | 8010 | 10 | hpa-stripe-key |
| hpa | cli | 3 of 3 | 1.7 | 743 | 18241 | 959 | 2.7 | 7754 | 9 | none |
| hpa | mcp | 3 of 3 | 3.0 | 639 | 19716 | 764 | 4.0 | 8193 | 8 | none |
| init | kubectl | 3 of 3 | 4.7 | 2050 | 38154 | 2057 | 5.7 | 11231 | 14 | init-bearer-token |
| init | rtk | 2 of 3 | 3.3 | 1910 | 28303 | 1556 | 4.3 | 10603 | 12 | init-bearer-token |
| init | cli | 0 of 3 | 2.0 | 687 | 20677 | 1456 | 3.0 | 7960 | 12 | none |
| init | mcp | 1 of 3 | 4.3 | 879 | 25784 | 1740 | 5.3 | 9148 | 12 | none |
| job | kubectl | 3 of 3 | 2.3 | 1218 | 23679 | 1551 | 3.3 | 8560 | 13 | job-github-token |
| job | rtk | 3 of 3 | 2.0 | 1129 | 20709 | 1169 | 3.0 | 8232 | 12 | job-github-token |
| job | cli | 3 of 3 | 2.0 | 1927 | 21038 | 1167 | 3.0 | 9668 | 10 | none |
| job | mcp | 3 of 3 | 4.3 | 887 | 25691 | 1159 | 5.3 | 8941 | 10 | none |
| netpol | kubectl | 3 of 3 | 2.7 | 4764 | 34422 | 1202 | 3.7 | 13306 | 11 | netpol-api-token |
| netpol | rtk | 3 of 3 | 3.3 | 2749 | 31611 | 1332 | 4.3 | 10818 | 11 | netpol-api-token |
| netpol | cli | 3 of 3 | 4.3 | 1578 | 37230 | 1690 | 5.3 | 10133 | 13 | none |
| netpol | mcp | 3 of 3 | 7.0 | 915 | 29026 | 1429 | 8.0 | 9324 | 11 | none |
| pvc | kubectl | 3 of 3 | 3.0 | 1400 | 29853 | 1260 | 4.0 | 8810 | 12 | none |
| pvc | rtk | 3 of 3 | 3.0 | 1278 | 29161 | 1122 | 4.0 | 8489 | 11 | none |
| pvc | cli | 3 of 3 | 1.7 | 580 | 18059 | 954 | 2.7 | 7473 | 9 | none |
| pvc | mcp | 3 of 3 | 2.0 | 388 | 14007 | 635 | 3.0 | 7466 | 6 | none |
| quota | kubectl | 3 of 3 | 3.3 | 886 | 29192 | 1179 | 4.3 | 8054 | 12 | none |
| quota | rtk | 3 of 3 | 3.3 | 1121 | 29979 | 1141 | 4.3 | 8257 | 11 | none |
| quota | cli | 3 of 3 | 2.3 | 997 | 23851 | 1152 | 3.3 | 8398 | 11 | none |
| quota | mcp | 3 of 3 | 4.0 | 614 | 22313 | 964 | 5.0 | 8304 | 9 | none |
| svcport | kubectl | 3 of 3 | 2.7 | 5155 | 35667 | 1424 | 3.7 | 14485 | 11 | svcport-db-password |
| svcport | rtk | 3 of 3 | 2.3 | 4401 | 33614 | 1392 | 3.3 | 13089 | 12 | svcport-db-password |
| svcport | cli | 3 of 3 | 2.7 | 1439 | 22923 | 1036 | 3.7 | 8975 | 11 | none |
| svcport | mcp | 3 of 3 | 8.0 | 3599 | 47108 | 1541 | 9.0 | 13754 | 12 | none |

## Wrong answers

- **init, rtk, run 3:** missing feature-flags; /ready/v2|404|not found. 
- **init, cli, run 1:** missing feature-flags; /ready/v2|404|not found. 
- **init, cli, run 2:** missing feature-flags; /ready/v2|404|not found. 
- **init, cli, run 3:** missing feature-flags; /ready/v2|404|not found. 
- **init, mcp, run 2:** missing feature-flags; /ready/v2|404|not found. 
- **init, mcp, run 3:** missing feature-flags; /ready/v2|404|not found. 
