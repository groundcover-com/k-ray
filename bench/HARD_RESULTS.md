# Hard benchmarks: output size, kubectl and kray

Measured 2026-11-15 on context `kind-kray-bench`. No LLM runs. Tokens are bytes / 4, an estimate. Each side runs the fixed command list in the task's `questions.json`.

**Answer in output** is whether every `must` group of the task matches the output. **Canaries** are the distinct planted secrets in the output, checked plain, base64 and URL-encoded. **Keep missing** counts the values a debugger needs that kubectl showed and kray did not.

## tokens

| Task | Side | Commands | Bytes | ~Tokens | Answer in output | Canaries | Keep missing |
|---|---|---:|---:|---:|---|---:|---|
| bad-release | kubectl | 4 | 6209 | 1552 | yes | 0 | - |
| bad-release | kray | 2 | 3511 | 877 | yes | 0 | 0 of 2 |
| bad-release | rtk | 4 | 34975 | 8743 | yes | 0 | 1 of 2 |
| stack-traces | kubectl | 3 | 3211 | 802 | yes | 0 | - |
| stack-traces | kray | 2 | 5438 | 1359 | yes | 0 | 0 of 4 |
| stack-traces | rtk | 3 | 347 | 86 | yes | 0 | 0 of 4 |
| stack-traces | kray-triage | 3 | 2548 | 637 | yes | 0 | 0 of 4 |
| stack-traces | kubectl-no-grep | 2 | 282560 | 70640 | yes | 0 | 0 of 4 |
| wrong-port | kubectl | 6 | 11396 | 2849 | yes | 0 | - |
| wrong-port | kray | 3 | 6259 | 1564 | yes | 0 | 0 of 2 |
| wrong-port | rtk | 6 | 9494 | 2373 | yes | 0 | 0 of 2 |

| Task | kubectl ~tokens | kray ~tokens | kubectl / kray |
|---|---:|---:|---:|
| bad-release | 1552 | 877 | 1.8x |
| stack-traces | 802 | 1359 | 0.6x |
| wrong-port | 2849 | 1564 | 1.8x |

## secrets

| Task | Side | Commands | Bytes | ~Tokens | Answer in output | Canaries | Keep missing |
|---|---|---:|---:|---:|---|---:|---|
| config-typo | kubectl | 4 | 14516 | 3629 | yes | 3 | - |
| config-typo | kray | 2 | 5755 | 1438 | yes | 0 | 0 of 4 |
| config-typo | rtk | 4 | 11508 | 2877 | yes | 3 | 0 of 4 |
| db-password | kubectl | 3 | 6041 | 1510 | yes | 1 | - |
| db-password | kray | 2 | 5832 | 1458 | yes | 0 | 0 of 6 |
| db-password | rtk | 3 | 4853 | 1213 | yes | 1 | 0 of 6 |
| upstream-403 | kubectl | 2 | 5618 | 1404 | yes | 1 | - |
| upstream-403 | kray | 2 | 4957 | 1239 | yes | 0 | 0 of 4 |
| upstream-403 | rtk | 2 | 5488 | 1372 | yes | 1 | 1 of 4 |

| Task | kubectl ~tokens | kray ~tokens | kubectl / kray |
|---|---:|---:|---:|
| config-typo | 3629 | 1438 | 2.5x |
| db-password | 1510 | 1458 | 1.0x |
| upstream-403 | 1404 | 1239 | 1.1x |

## Details

### tokens / bad-release

- **kubectl:** canaries: none. Must groups not matched: none. Commands with a non-zero exit: kubectl rollout status deployment/storefront -n shop-web --timeout=5s. Keep values missing: none.
- **kray:** canaries: none. Must groups not matched: none. Commands with a non-zero exit: none. Keep values missing: none.
- **rtk:** canaries: none. Must groups not matched: none. Commands with a non-zero exit: kubectl rollout status deployment/storefront -n shop-web --timeout=5s. Keep values missing: probe-status.
- **keep values kubectl showed:** bad-probe-path, probe-status.

### tokens / stack-traces

- **kubectl:** canaries: none. Must groups not matched: none. Commands with a non-zero exit: none. Keep values missing: none.
- **kray:** canaries: none. Must groups not matched: none. Commands with a non-zero exit: none. Keep values missing: none.
- **rtk:** canaries: none. Must groups not matched: none. Commands with a non-zero exit: kubectl get pods -A | grep -i checkout. Keep values missing: none.
- **kray-triage:** canaries: none. Must groups not matched: none. Commands with a non-zero exit: for i in $(seq 0 19); do kray logs checkout-$i -n shop-orders --tail 500 | grep -q '^errors:' && echo "checkout-$i has errors"; done. Keep values missing: none.
- **kubectl-no-grep:** canaries: none. Must groups not matched: none. Commands with a non-zero exit: none. Keep values missing: none.
- **keep values kubectl showed:** gateway-host, faulty-pod-3, faulty-pod-10, faulty-pod-17.

### tokens / wrong-port

- **kubectl:** canaries: none. Must groups not matched: none. Commands with a non-zero exit: none. Keep values missing: none.
- **kray:** canaries: none. Must groups not matched: none. Commands with a non-zero exit: none. Keep values missing: none.
- **rtk:** canaries: none. Must groups not matched: none. Commands with a non-zero exit: none. Keep values missing: none.
- **keep values kubectl showed:** service-target-port, upstream-host.

### secrets / config-typo

- **kubectl:** canaries: basic-auth-url-password, pem-private-key, plain-env-password. Must groups not matched: none. Commands with a non-zero exit: none. Keep values missing: none.
- **kray:** canaries: none. Must groups not matched: none. Commands with a non-zero exit: none. Keep values missing: none.
- **rtk:** canaries: basic-auth-url-password, pem-private-key, plain-env-password. Must groups not matched: none. Commands with a non-zero exit: kubectl get pods -A | grep -E ' 0/'. Keep values missing: none.
- **keep values kubectl showed:** typo-host, statements-git-sha, statements-pod, config-map.

### secrets / db-password

- **kubectl:** canaries: postgres-password. Must groups not matched: none. Commands with a non-zero exit: none. Keep values missing: none.
- **kray:** canaries: none. Must groups not matched: none. Commands with a non-zero exit: none. Keep values missing: none.
- **rtk:** canaries: postgres-password. Must groups not matched: none. Commands with a non-zero exit: none. Keep values missing: none.
- **keep values kubectl showed:** ledger-git-sha, image-digest, ledger-request-id, db-host, db-user, ledger-pod.

### secrets / upstream-403

- **kubectl:** canaries: bearer-token. Must groups not matched: none. Commands with a non-zero exit: none. Keep values missing: none.
- **kray:** canaries: none. Must groups not matched: none. Commands with a non-zero exit: none. Keep values missing: none.
- **rtk:** canaries: bearer-token. Must groups not matched: none. Commands with a non-zero exit: kubectl get pods -A | grep -i gateway. Keep values missing: gateway-pod.
- **keep values kubectl showed:** forbidden-path, forbidden-request-id, upstream-host, gateway-pod.

