# Agent benchmark

Measured 2026-11-16. Model: `haiku`. Runs: 60.

Tool tokens are the bytes of tool output the model read, divided by 4. Input tokens are what the CLI reports, summed over all turns with cache reads and writes included, so one big tool result is counted again on every later turn. Peak context is the largest single turn's input. Canaries are the distinct planted secrets in the transcript, in plain, base64 or URL-encoded form.

## Totals per arm

| Arm | Correct | Tool calls (mean) | Tool tokens (mean) | Input tokens (mean) | Output tokens (mean) | Turns (mean) | Peak context (mean) | Seconds (mean) | Cost (mean) | Distinct canaries seen (mean) | Runs that saw a secret |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cli | 29 of 30 | 2.8 | 963 | 24900 | 1279 | 3.8 | 8471 | 11 | $0.002 | 0.0 | 0 |
| mcp | 29 of 30 | 3.8 | 833 | 24104 | 1164 | 4.8 | 8804 | 9 | $0.002 | 0.0 | 0 |

## Per scenario

| Scenario | Arm | Correct | Tool calls | Tool tokens | Input tokens | Output tokens | Turns | Peak context | Seconds | Canaries seen |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| batch-notexec | cli | 3 of 3 | 2.3 | 693 | 23462 | 1148 | 3.3 | 7973 | 12 | none |
| batch-notexec | mcp | 3 of 3 | 2.3 | 459 | 19334 | 901 | 3.3 | 7851 | 8 | none |
| billing-limitrange | cli | 2 of 3 | 1.7 | 509 | 18015 | 1150 | 2.7 | 7412 | 10 | none |
| billing-limitrange | mcp | 2 of 3 | 3.0 | 604 | 19905 | 918 | 4.0 | 8194 | 8 | none |
| catalog-paused | cli | 3 of 3 | 3.3 | 764 | 25846 | 1087 | 4.3 | 8125 | 10 | none |
| catalog-paused | mcp | 3 of 3 | 3.3 | 673 | 22195 | 895 | 4.3 | 8297 | 8 | none |
| mailer-readonlyfs | cli | 3 of 3 | 3.0 | 982 | 24266 | 1178 | 4.0 | 8576 | 11 | none |
| mailer-readonlyfs | mcp | 3 of 3 | 3.0 | 782 | 22859 | 945 | 4.0 | 8617 | 8 | none |
| media-memquota | cli | 3 of 3 | 2.7 | 643 | 21022 | 1163 | 3.7 | 7867 | 10 | none |
| media-memquota | mcp | 3 of 3 | 3.0 | 669 | 19867 | 1005 | 4.0 | 8232 | 8 | none |
| orders-externalname | cli | 3 of 3 | 3.0 | 1035 | 29359 | 1289 | 4.0 | 8668 | 11 | none |
| orders-externalname | mcp | 3 of 3 | 5.7 | 968 | 32151 | 1302 | 6.7 | 9287 | 10 | none |
| portal-sidecar | cli | 3 of 3 | 2.3 | 780 | 23470 | 1246 | 3.3 | 8114 | 11 | none |
| portal-sidecar | mcp | 3 of 3 | 2.7 | 848 | 20201 | 876 | 3.7 | 8503 | 7 | none |
| render-startupprobe | cli | 3 of 3 | 2.7 | 1695 | 29064 | 1915 | 3.7 | 10124 | 14 | none |
| render-startupprobe | mcp | 3 of 3 | 4.3 | 1806 | 32635 | 2331 | 5.3 | 11318 | 15 | none |
| ring-subdomain | cli | 3 of 3 | 4.3 | 2011 | 34275 | 1579 | 5.3 | 10352 | 13 | none |
| ring-subdomain | mcp | 3 of 3 | 8.3 | 1011 | 32459 | 1727 | 9.3 | 9905 | 11 | none |
| search-cmkey | cli | 3 of 3 | 2.3 | 516 | 20219 | 1036 | 3.3 | 7500 | 10 | none |
| search-cmkey | mcp | 3 of 3 | 2.7 | 505 | 19435 | 742 | 3.7 | 7834 | 8 | none |

## Wrong answers

- **billing-limitrange, cli, run 2:** missing limitrange|limit range|container-caps. 
- **billing-limitrange, mcp, run 1:** missing limitrange|limit range|container-caps. 
