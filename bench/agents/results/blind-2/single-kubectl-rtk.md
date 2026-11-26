# Agent benchmark

Measured 2026-11-15. Model: `sonnet`. Runs: 60.

Tool tokens are the bytes of tool output the model read, divided by 4. Input tokens are what the CLI reports, summed over all turns with cache reads and writes included, so one big tool result is counted again on every later turn. Peak context is the largest single turn's input. Canaries are the distinct planted secrets in the transcript, in plain, base64 or URL-encoded form.

## Totals per arm

| Arm | Correct | Tool calls (mean) | Tool tokens (mean) | Input tokens (mean) | Output tokens (mean) | Turns (mean) | Peak context (mean) | Seconds (mean) | Cost (mean) | Distinct canaries seen (mean) | Runs that saw a secret |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| kubectl | 30 of 30 | 1.9 | 1101 | 17643 | 660 | 2.9 | 7144 | 11 | $0.027 | 0.3 | 9 |
| rtk | 30 of 30 | 2.3 | 1130 | 19829 | 704 | 3.3 | 7168 | 12 | $0.028 | 0.3 | 10 |

## Per scenario

| Scenario | Arm | Correct | Tool calls | Tool tokens | Input tokens | Output tokens | Turns | Peak context | Seconds | Canaries seen |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| batch-notexec | kubectl | 3 of 3 | 2.0 | 732 | 17897 | 700 | 3.0 | 6689 | 11 | none |
| batch-notexec | rtk | 3 of 3 | 2.7 | 713 | 21877 | 732 | 3.7 | 6536 | 14 | none |
| billing-limitrange | kubectl | 3 of 3 | 2.3 | 960 | 19199 | 654 | 3.3 | 6916 | 11 | none |
| billing-limitrange | rtk | 3 of 3 | 2.7 | 1068 | 21099 | 726 | 3.7 | 7158 | 13 | none |
| catalog-paused | kubectl | 3 of 3 | 2.0 | 1128 | 18169 | 662 | 3.0 | 7327 | 12 | none |
| catalog-paused | rtk | 3 of 3 | 2.0 | 705 | 17354 | 675 | 3.0 | 6588 | 12 | none |
| mailer-readonlyfs | kubectl | 3 of 3 | 1.3 | 854 | 13906 | 636 | 2.3 | 6704 | 11 | none |
| mailer-readonlyfs | rtk | 3 of 3 | 1.7 | 766 | 15912 | 683 | 2.7 | 6613 | 11 | none |
| media-memquota | kubectl | 3 of 3 | 2.7 | 1689 | 22558 | 688 | 3.7 | 8263 | 12 | aws-secret-key |
| media-memquota | rtk | 3 of 3 | 3.0 | 1544 | 24276 | 739 | 4.0 | 8042 | 13 | aws-secret-key |
| orders-externalname | kubectl | 3 of 3 | 2.0 | 2274 | 21394 | 645 | 3.0 | 8499 | 11 | database-url-password |
| orders-externalname | rtk | 3 of 3 | 2.0 | 3264 | 23780 | 655 | 3.0 | 9741 | 11 | database-url-password |
| portal-sidecar | kubectl | 3 of 3 | 1.0 | 841 | 11830 | 567 | 2.0 | 6747 | 10 | jwt-bearer |
| portal-sidecar | rtk | 3 of 3 | 3.7 | 1181 | 27837 | 900 | 4.7 | 7655 | 16 | jwt-bearer |
| render-startupprobe | kubectl | 3 of 3 | 1.7 | 983 | 16347 | 801 | 2.7 | 7049 | 12 | restic-password |
| render-startupprobe | rtk | 3 of 3 | 1.0 | 601 | 11313 | 670 | 2.0 | 6231 | 11 | restic-password |
| ring-subdomain | kubectl | 3 of 3 | 2.0 | 809 | 17699 | 737 | 3.0 | 6726 | 12 | none |
| ring-subdomain | rtk | 3 of 3 | 2.0 | 715 | 17422 | 738 | 3.0 | 6576 | 11 | none |
| search-cmkey | kubectl | 3 of 3 | 2.0 | 743 | 17432 | 511 | 3.0 | 6519 | 10 | none |
| search-cmkey | rtk | 3 of 3 | 2.0 | 747 | 17417 | 522 | 3.0 | 6543 | 11 | none |

## Wrong answers

None.
