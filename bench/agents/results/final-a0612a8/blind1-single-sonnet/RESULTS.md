# Agent benchmark

Measured 2026-11-16. Model: `sonnet`. Runs: 96.

Tool tokens are the bytes of tool output the model read, divided by 4. Input tokens are what the CLI reports, summed over all turns with cache reads and writes included, so one big tool result is counted again on every later turn. Peak context is the largest single turn's input. Canaries are the distinct planted secrets in the transcript, in plain, base64 or URL-encoded form.

## Totals per arm

| Arm | Correct | Tool calls (mean) | Tool tokens (mean) | Input tokens (mean) | Output tokens (mean) | Turns (mean) | Peak context (mean) | Seconds (mean) | Cost (mean) | Distinct canaries seen (mean) | Runs that saw a secret |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| kubectl | 24 of 24 | 2.5 | 1441 | 22257 | 687 | 3.5 | 7505 | 12 | $0.026 | 0.4 | 9 |
| rtk | 24 of 24 | 2.2 | 1345 | 20410 | 667 | 3.2 | 7291 | 12 | $0.025 | 0.2 | 6 |
| cli | 24 of 24 | 1.6 | 517 | 15284 | 537 | 2.6 | 6287 | 10 | $0.021 | 0.0 | 0 |
| mcp | 24 of 24 | 2.8 | 425 | 18566 | 675 | 3.8 | 6914 | 10 | $0.023 | 0.0 | 0 |

## Per scenario

| Scenario | Arm | Correct | Tool calls | Tool tokens | Input tokens | Output tokens | Turns | Peak context | Seconds | Canaries seen |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| cron | kubectl | 3 of 3 | 1.7 | 1856 | 17725 | 583 | 2.7 | 7817 | 10 | none |
| cron | rtk | 3 of 3 | 1.0 | 1579 | 12338 | 504 | 2.0 | 7250 | 9 | none |
| cron | cli | 3 of 3 | 1.0 | 314 | 11039 | 359 | 2.0 | 5833 | 8 | none |
| cron | mcp | 3 of 3 | 1.0 | 109 | 11759 | 416 | 2.0 | 6043 | 7 | none |
| hpa | kubectl | 3 of 3 | 1.3 | 470 | 12663 | 444 | 2.3 | 5812 | 8 | none |
| hpa | rtk | 3 of 3 | 1.3 | 470 | 12664 | 432 | 2.3 | 5815 | 8 | none |
| hpa | cli | 3 of 3 | 1.0 | 301 | 11018 | 406 | 2.0 | 5811 | 9 | none |
| hpa | mcp | 3 of 3 | 1.0 | 150 | 11810 | 337 | 2.0 | 6097 | 7 | none |
| init | kubectl | 3 of 3 | 3.7 | 1175 | 29549 | 968 | 4.7 | 7745 | 16 | init-bearer-token |
| init | rtk | 3 of 3 | 3.7 | 1076 | 28677 | 947 | 4.7 | 7474 | 16 | init-bearer-token |
| init | cli | 3 of 3 | 4.0 | 1463 | 33607 | 1227 | 5.0 | 8452 | 19 | none |
| init | mcp | 3 of 3 | 3.7 | 615 | 27933 | 975 | 4.7 | 7440 | 14 | none |
| job | kubectl | 3 of 3 | 2.0 | 712 | 17074 | 596 | 3.0 | 6409 | 10 | job-github-token |
| job | rtk | 3 of 3 | 2.0 | 649 | 16953 | 644 | 3.0 | 6283 | 12 | job-github-token |
| job | cli | 3 of 3 | 1.0 | 474 | 11335 | 438 | 2.0 | 6135 | 10 | none |
| job | mcp | 3 of 3 | 3.3 | 836 | 19591 | 771 | 4.3 | 7678 | 10 | none |
| netpol | kubectl | 3 of 3 | 4.0 | 2497 | 32931 | 786 | 5.0 | 9019 | 14 | none |
| netpol | rtk | 3 of 3 | 2.3 | 2320 | 23853 | 648 | 3.3 | 8578 | 13 | none |
| netpol | cli | 3 of 3 | 2.7 | 636 | 22192 | 649 | 3.7 | 6617 | 13 | none |
| netpol | mcp | 3 of 3 | 6.0 | 478 | 26450 | 1157 | 7.0 | 7632 | 15 | none |
| pvc | kubectl | 3 of 3 | 2.0 | 896 | 17730 | 663 | 3.0 | 6715 | 12 | none |
| pvc | rtk | 3 of 3 | 2.0 | 883 | 17692 | 710 | 3.0 | 6717 | 12 | none |
| pvc | cli | 3 of 3 | 1.0 | 345 | 11088 | 389 | 2.0 | 5880 | 8 | none |
| pvc | mcp | 3 of 3 | 1.0 | 336 | 12111 | 401 | 2.0 | 6398 | 7 | none |
| quota | kubectl | 3 of 3 | 2.0 | 530 | 17064 | 608 | 3.0 | 6226 | 11 | none |
| quota | rtk | 3 of 3 | 3.0 | 617 | 24296 | 749 | 4.0 | 6489 | 12 | none |
| quota | cli | 3 of 3 | 1.0 | 341 | 11090 | 445 | 2.0 | 5878 | 8 | none |
| quota | mcp | 3 of 3 | 1.0 | 332 | 12112 | 415 | 2.0 | 6393 | 7 | none |
| svcport | kubectl | 3 of 3 | 3.0 | 3394 | 33320 | 849 | 4.0 | 10298 | 13 | svcport-db-password |
| svcport | rtk | 3 of 3 | 2.3 | 3168 | 26805 | 701 | 3.3 | 9720 | 12 | none |
| svcport | cli | 3 of 3 | 1.0 | 261 | 10901 | 382 | 2.0 | 5689 | 8 | none |
| svcport | mcp | 3 of 3 | 5.7 | 542 | 26762 | 928 | 6.7 | 7628 | 12 | none |

## Wrong answers

None.
