# Output size: kubectl and this server

Measured 2026-11-13 on the cluster in `test/integration/out/kubeconfig`, namespace `fixture-5841c7`.
Tokens are estimated as bytes / 4.

| What | kubectl bytes | kubectl ~tokens | server bytes | server ~tokens | ratio |
|---|---:|---:|---:|---:|---:|
| list pods, all namespaces | 2121 | 530 | 1931 | 482 | 1.1x |
| list pods, all namespaces, as YAML | 64006 | 16001 | 1931 | 482 | 33.1x |
| get deployment api | 3129 | 782 | 2310 | 577 | 1.4x |
| describe deployment api | 2300 | 575 | 2573 | 643 | 0.9x |
| describe pod api-766b7bcddd-pz64b | 3604 | 901 | 4628 | 1157 | 0.8x |
| logs api-766b7bcddd-pz64b (tail 2000) | 364 | 91 | 855 | 213 | 0.4x |
| events, all namespaces | 12616 | 3154 | 8611 | 2152 | 1.5x |
| get node kray-harness-control-plane | 4934 | 1233 | 2979 | 744 | 1.7x |

The server output includes its header, the data fence, notes and redaction markers. kubectl output is shown with its default flags (kubectl hides managedFields by default).
##[group]Run kubectl --context kind-kray get pods -A -o wide | tee artifacts/pods.txt || true

CI run: https://github.com/groundcover-com/kray/actions/runs/43430353430 (kind v0.33.0, Kubernetes v1.37.0).
