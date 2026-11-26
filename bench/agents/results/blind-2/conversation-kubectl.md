# Agent benchmark

Measured 2026-11-15. Model: `sonnet`. Runs: 3.

Tool tokens are the bytes of tool output the model read, divided by 4. Input tokens are what the CLI reports, summed over all turns with cache reads and writes included, so one big tool result is counted again on every later turn. Peak context is the largest single turn's input. Canaries are the distinct planted secrets in the transcript, in plain, base64 or URL-encoded form.

## Totals per arm

| Arm | Correct | Tool calls (mean) | Tool tokens (mean) | Input tokens (mean) | Output tokens (mean) | Turns (mean) | Peak context (mean) | Seconds (mean) | Cost (mean) | Distinct canaries seen (mean) | Runs that saw a secret |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| kubectl | 3 of 3 | 27.0 | 7850 | 569278 | 8330 | 37.0 | 27126 | 139 | $1.315 | 2.0 | 3 |

## Per scenario

| Scenario | Arm | Correct | Tool calls | Tool tokens | Input tokens | Output tokens | Turns | Peak context | Seconds | Canaries seen |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| session | kubectl | 3 of 3 | 27.0 | 7850 | 569278 | 8330 | 37.0 | 27126 | 139 | database-url-password, jwt-bearer |

## Wrong answers

None.
