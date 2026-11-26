#!/usr/bin/env bash
# Kind cluster for the canary harness (test/integration).
#
#   scripts/kind.sh up              create the cluster with API server audit logging
#   scripts/kind.sh metrics-server  install the pinned metrics-server
#   scripts/kind.sh audit-log       print the API server audit log
#   scripts/kind.sh down            delete the cluster
#
# kind comes from PATH when its version matches, else from `go run` at the
# pinned version. Needs Docker; metrics-server also needs kubectl.
#
# KIND_NODE_IMAGE selects the Kubernetes version of a new cluster, for
# example kindest/node:v1.35.8@sha256:... (ci runs one per supported minor).
# The default is the node image of the pinned kind release. An existing
# cluster with the same name is reused as it is.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

NAME="${KIND_CLUSTER_NAME:-kray-harness}"
KIND_VERSION="v0.33.0"
NODE_IMAGE="${KIND_NODE_IMAGE:-kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5}"
METRICS_SERVER_VERSION="v0.9.1"
METRICS_SERVER_SHA256="3c7f57cdbee0f57fb0259a150e8f6d6cb4f29d72d1aa5dce8803151dd6d4c2a1"
OUT="test/integration/out"

kind() {
	if type -P kind >/dev/null 2>&1 && [[ "$(command kind version | awk '{print $2}')" == "$KIND_VERSION" ]]; then
		command kind "$@"
	else
		go run "sigs.k8s.io/kind@${KIND_VERSION}" "$@"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum -- "$1" | awk '{print $1}'
	else
		shasum -a 256 -- "$1" | awk '{print $1}'
	fi
}

up() {
	mkdir -p "$OUT"
	local policy
	policy="$(pwd)/test/integration/kind/audit-policy.yaml"
	sed "s|@POLICY@|${policy}|" test/integration/kind/cluster.yaml >"$OUT/cluster.yaml"
	if ! kind get clusters 2>/dev/null | grep -qx "$NAME"; then
		kind create cluster --name "$NAME" --image "$NODE_IMAGE" --config "$OUT/cluster.yaml" --wait 180s
	fi
	kind get kubeconfig --name "$NAME" >"$OUT/kubeconfig"
	chmod 600 "$OUT/kubeconfig"
	echo "cluster ${NAME} is up; admin kubeconfig: ${OUT}/kubeconfig"
}

metrics_server() {
	mkdir -p "$OUT"
	local f="$OUT/metrics-server.yaml"
	curl -fsSL --retry 5 -o "$f" "https://github.com/kubernetes-sigs/metrics-server/releases/download/${METRICS_SERVER_VERSION}/components.yaml"
	if [[ "$(sha256 "$f")" != "$METRICS_SERVER_SHA256" ]]; then
		echo "metrics-server manifest checksum mismatch" >&2
		exit 1
	fi
	local kc=(--kubeconfig "$OUT/kubeconfig")
	kubectl "${kc[@]}" apply -f "$f"
	# kind kubelets use self-signed serving certificates.
	if ! kubectl "${kc[@]}" -n kube-system get deployment metrics-server -o jsonpath='{.spec.template.spec.containers[0].args}' | grep -q kubelet-insecure-tls; then
		kubectl "${kc[@]}" -n kube-system patch deployment metrics-server --type=json \
			-p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
	fi
	kubectl "${kc[@]}" -n kube-system rollout status deployment/metrics-server --timeout=240s
}

audit_log() {
	docker exec "${NAME}-control-plane" cat /var/log/kubernetes/audit/audit.log
}

down() {
	kind delete cluster --name "$NAME"
}

case "${1:-}" in
up) up ;;
metrics-server) metrics_server ;;
audit-log) audit_log ;;
down) down ;;
*)
	echo "usage: $0 up|metrics-server|audit-log|down" >&2
	exit 2
	;;
esac
