# Agent benchmark

Measured 2026-11-15. Model: `sonnet`. Runs: 24.

Tool tokens are the bytes of tool output the model read, divided by 4. Input tokens are what the CLI reports, summed over all turns with cache reads and writes included, so one big tool result is counted again on every later turn. Peak context is the largest single turn's input. Canaries are the distinct planted secrets in the transcript, in plain, base64 or URL-encoded form.

## Totals per arm

| Arm | Correct | Tool calls (mean) | Tool tokens (mean) | Input tokens (mean) | Output tokens (mean) | Turns (mean) | Peak context (mean) | Seconds (mean) | Cost (mean) | Distinct canaries seen (mean) | Runs that saw a secret |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| kubectl | 6 of 6 | 4.2 | 6305 | 356652 | 1392 | 5.2 | 71949 | 22 | $0.146 | 0.0 | 0 |
| rtk | 6 of 6 | 4.7 | 5398 | 382960 | 1623 | 5.7 | 70135 | 26 | $0.144 | 0.0 | 0 |
| cli | 6 of 6 | 2.5 | 1988 | 210460 | 805 | 3.5 | 64835 | 15 | $0.098 | 0.0 | 0 |
| mcp | 6 of 6 | 4.7 | 2893 | 280770 | 974 | 5.7 | 70360 | 16 | $0.115 | 0.0 | 0 |

## Per scenario

| Scenario | Arm | Correct | Tool calls | Tool tokens | Input tokens | Output tokens | Turns | Peak context | Seconds | Canaries seen |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| bad-release | kubectl | 2 of 2 | 2.0 | 2113 | 187828 | 830 | 3.0 | 64298 | 14 | none |
| bad-release | rtk | 2 of 2 | 2.5 | 2134 | 220142 | 914 | 3.5 | 64517 | 16 | none |
| bad-release | cli | 2 of 2 | 2.0 | 2110 | 189359 | 698 | 3.0 | 64956 | 14 | none |
| bad-release | mcp | 2 of 2 | 4.0 | 2489 | 201534 | 756 | 5.0 | 69476 | 11 | none |
| stack-traces | kubectl | 2 of 2 | 7.0 | 11315 | 585390 | 2320 | 8.0 | 82489 | 36 | none |
| stack-traces | rtk | 2 of 2 | 8.5 | 8750 | 664740 | 2894 | 9.5 | 76866 | 44 | none |
| stack-traces | cli | 2 of 2 | 3.5 | 1982 | 252706 | 1124 | 4.5 | 65032 | 18 | none |
| stack-traces | mcp | 2 of 2 | 6.0 | 4188 | 372784 | 1304 | 7.0 | 72941 | 21 | none |
| wrong-port | kubectl | 2 of 2 | 3.5 | 5488 | 296738 | 1026 | 4.5 | 69062 | 16 | none |
| wrong-port | rtk | 2 of 2 | 3.0 | 5311 | 263998 | 1062 | 4.0 | 69022 | 17 | none |
| wrong-port | cli | 2 of 2 | 2.0 | 1872 | 189316 | 592 | 3.0 | 64518 | 13 | none |
| wrong-port | mcp | 2 of 2 | 4.0 | 2002 | 267990 | 861 | 5.0 | 68662 | 16 | none |

## Wrong answers

None.
