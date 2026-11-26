# Agent benchmark

Measured 2026-11-16. Model: `sonnet`. Runs: 90.

Tool tokens are the bytes of tool output the model read, divided by 4. Input tokens are what the CLI reports, summed over all turns with cache reads and writes included, so one big tool result is counted again on every later turn. Peak context is the largest single turn's input. Canaries are the distinct planted secrets in the transcript, in plain, base64 or URL-encoded form.

## Totals per arm

| Arm | Correct | Tool calls (mean) | Tool tokens (mean) | Input tokens (mean) | Output tokens (mean) | Turns (mean) | Peak context (mean) | Seconds (mean) | Cost (mean) | Distinct canaries seen (mean) | Runs that saw a secret |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| kubectl | 30 of 30 | 2.0 | 1119 | 18085 | 661 | 3.0 | 7227 | 11 | $0.028 | 0.4 | 12 |
| cli | 30 of 30 | 1.7 | 533 | 15950 | 549 | 2.7 | 6358 | 11 | $0.023 | 0.0 | 0 |
| mcp | 30 of 30 | 2.2 | 462 | 16850 | 645 | 3.2 | 6866 | 9 | $0.025 | 0.0 | 0 |

## Per scenario

| Scenario | Arm | Correct | Tool calls | Tool tokens | Input tokens | Output tokens | Turns | Peak context | Seconds | Canaries seen |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| batch-notexec | kubectl | 3 of 3 | 2.0 | 725 | 17947 | 671 | 3.0 | 6666 | 11 | none |
| batch-notexec | cli | 3 of 3 | 2.0 | 427 | 17301 | 498 | 3.0 | 6189 | 10 | none |
| batch-notexec | mcp | 3 of 3 | 3.0 | 563 | 19766 | 700 | 4.0 | 7185 | 11 | none |
| billing-limitrange | kubectl | 3 of 3 | 2.0 | 810 | 17042 | 615 | 3.0 | 6629 | 12 | none |
| billing-limitrange | cli | 3 of 3 | 1.0 | 347 | 11108 | 505 | 2.0 | 5890 | 9 | none |
| billing-limitrange | mcp | 3 of 3 | 1.7 | 384 | 16600 | 608 | 2.7 | 6595 | 9 | none |
| catalog-paused | kubectl | 3 of 3 | 2.3 | 1071 | 19696 | 683 | 3.3 | 7174 | 12 | none |
| catalog-paused | cli | 3 of 3 | 2.0 | 704 | 17651 | 666 | 3.0 | 6758 | 12 | none |
| catalog-paused | mcp | 3 of 3 | 1.3 | 343 | 14288 | 475 | 2.3 | 6489 | 8 | none |
| mailer-readonlyfs | kubectl | 3 of 3 | 2.0 | 742 | 18076 | 686 | 3.0 | 6609 | 12 | none |
| mailer-readonlyfs | cli | 3 of 3 | 1.7 | 368 | 15220 | 558 | 2.7 | 6077 | 11 | none |
| mailer-readonlyfs | mcp | 3 of 3 | 3.3 | 544 | 23570 | 846 | 4.3 | 7292 | 11 | none |
| media-memquota | kubectl | 3 of 3 | 2.0 | 1410 | 18133 | 633 | 3.0 | 7717 | 12 | aws-secret-key |
| media-memquota | cli | 3 of 3 | 1.0 | 426 | 11259 | 453 | 2.0 | 6057 | 9 | none |
| media-memquota | mcp | 3 of 3 | 1.0 | 445 | 12322 | 460 | 2.0 | 6616 | 7 | none |
| orders-externalname | kubectl | 3 of 3 | 2.0 | 2497 | 21986 | 659 | 3.0 | 9141 | 11 | database-url-password |
| orders-externalname | cli | 3 of 3 | 3.0 | 604 | 23755 | 541 | 4.0 | 6577 | 11 | none |
| orders-externalname | mcp | 3 of 3 | 3.0 | 377 | 18968 | 660 | 4.0 | 6786 | 9 | none |
| portal-sidecar | kubectl | 3 of 3 | 1.7 | 1021 | 15767 | 615 | 2.7 | 7100 | 9 | jwt-bearer |
| portal-sidecar | cli | 3 of 3 | 1.0 | 445 | 11320 | 444 | 2.0 | 6114 | 9 | none |
| portal-sidecar | mcp | 3 of 3 | 1.0 | 472 | 12421 | 573 | 2.0 | 6707 | 7 | none |
| render-startupprobe | kubectl | 3 of 3 | 1.7 | 1129 | 16219 | 757 | 2.7 | 7269 | 12 | restic-password |
| render-startupprobe | cli | 3 of 3 | 2.0 | 824 | 18259 | 841 | 3.0 | 7067 | 14 | none |
| render-startupprobe | mcp | 3 of 3 | 2.0 | 444 | 18661 | 790 | 3.0 | 6843 | 11 | none |
| ring-subdomain | kubectl | 3 of 3 | 2.0 | 1088 | 18616 | 761 | 3.0 | 7513 | 10 | none |
| ring-subdomain | cli | 3 of 3 | 2.7 | 846 | 22543 | 625 | 3.7 | 6960 | 10 | none |
| ring-subdomain | mcp | 3 of 3 | 4.7 | 673 | 19690 | 905 | 5.7 | 7649 | 11 | none |
| search-cmkey | kubectl | 3 of 3 | 2.0 | 696 | 17373 | 531 | 3.0 | 6453 | 10 | none |
| search-cmkey | cli | 3 of 3 | 1.0 | 336 | 11090 | 357 | 2.0 | 5890 | 9 | none |
| search-cmkey | mcp | 3 of 3 | 1.0 | 371 | 12212 | 434 | 2.0 | 6503 | 7 | none |

## Wrong answers

None.
