# Agent benchmark

Measured 2026-11-16. Model: `sonnet`. Runs: 8.

Tool tokens are the bytes of tool output the model read, divided by 4. Input tokens are what the CLI reports, summed over all turns with cache reads and writes included, so one big tool result is counted again on every later turn. Peak context is the largest single turn's input. Canaries are the distinct planted secrets in the transcript, in plain, base64 or URL-encoded form.

## Totals per arm

| Arm | Correct | Tool calls (mean) | Tool tokens (mean) | Input tokens (mean) | Output tokens (mean) | Turns (mean) | Peak context (mean) | Seconds (mean) | Cost (mean) | Distinct canaries seen (mean) | Runs that saw a secret |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| kubectl | 2 of 2 | 23.0 | 6236 | 345220 | 6654 | 31.0 | 22017 | 101 | $0.835 | 2.0 | 2 |
| rtk | 2 of 2 | 18.5 | 5842 | 341774 | 6476 | 26.5 | 21052 | 104 | $0.835 | 2.0 | 2 |
| cli | 2 of 2 | 16.5 | 6094 | 306484 | 5441 | 24.5 | 21450 | 99 | $0.816 | 0.0 | 0 |
| mcp | 2 of 2 | 28.5 | 5460 | 327932 | 6452 | 36.5 | 22712 | 90 | $0.842 | 0.0 | 0 |

## Per scenario

| Scenario | Arm | Correct | Tool calls | Tool tokens | Input tokens | Output tokens | Turns | Peak context | Seconds | Canaries seen |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| session | kubectl | 2 of 2 | 23.0 | 6236 | 345220 | 6654 | 31.0 | 22017 | 101 | init-bearer-token, job-github-token |
| session | rtk | 2 of 2 | 18.5 | 5842 | 341774 | 6476 | 26.5 | 21052 | 104 | init-bearer-token, job-github-token |
| session | cli | 2 of 2 | 16.5 | 6094 | 306484 | 5441 | 24.5 | 21450 | 99 | none |
| session | mcp | 2 of 2 | 28.5 | 5460 | 327932 | 6452 | 36.5 | 22712 | 90 | none |

## Wrong answers

None.
