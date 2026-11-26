#!/usr/bin/env bash
# Cursor Cloud Agent install step (.cursor/environment.json). Provisions what CI and the kind harness need on top of the default
# Ubuntu image: Docker, kind, kubectl, strace, shellcheck, the pinned Go
# toolchain and the module cache. Idempotent: every step is skipped when
# already satisfied.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Same versions as .github/workflows/ci.yml (helm/kind-action).
KIND_VERSION="${KIND_VERSION:-v0.33.0}"
KUBECTL_VERSION="${KUBECTL_VERSION:-v1.37.2}"

# Official SHA-256 of the linux downloads. Bump the matching row when you
# change KIND_VERSION, KUBECTL_VERSION, or the go.mod toolchain line.
# Sources: go.dev/dl/?mode=json, kind GitHub release *.sha256sum,
# dl.k8s.io/release/<ver>/bin/linux/<arch>/kubectl.sha256.
kind_sha256() {
	case "$1" in
	amd64) echo aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d ;;
	arm64) echo 20022bee6cfcd5086cb7234d218e3454e6090022f2a8f55d1fa7fcf42c3867a2 ;;
	*) return 1 ;;
	esac
}

kubectl_sha256() {
	case "$1" in
	amd64) echo c04487523e7aa7d1493f7808174c899db93d7a9cf57531420cd76b9bd415f8f4 ;;
	arm64) echo fcc16f63186659d9722014b7e02d5fdd2d4e669cb8c0ab66365d7d3b8ba03509 ;;
	*) return 1 ;;
	esac
}

go_sha256() {
	case "$1-$2" in
	1.27.4-amd64) echo 7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840 ;;
	1.27.4-arm64) echo 3392582988a273a87ab5043ee1b2c9c17a2c3c00580bf9326c47a2e0aaaeac92 ;;
	*)
		echo "no pinned checksum for go $1 linux/$2; add it next to go.mod" >&2
		return 1
		;;
	esac
}

host_arch() {
	case "$(uname -m)" in
	x86_64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	*)
		echo "unsupported architecture: $(uname -m)" >&2
		return 1
		;;
	esac
}

fetch() {
	local url="$1" dest="$2"
	local attempt delay
	for attempt in 1 2 3 4 5; do
		if curl -fsSL --retry 5 --retry-delay 2 --connect-timeout 20 --max-time 120 \
			-o "$dest" "$url"; then
			return 0
		fi
		if ((attempt == 5)); then
			echo "download failed after 5 attempts: $url" >&2
			return 1
		fi
		delay=$((attempt * 4))
		echo "download failed (attempt ${attempt}/5); retrying in ${delay}s: $url" >&2
		sleep "$delay"
	done
}

verify_sha256() {
	local file="$1" want="$2"
	local got
	got="$(sha256sum -- "$file" | awk '{print $1}')"
	if [[ "$got" != "$want" ]]; then
		echo "checksum mismatch ${file}: got ${got} want ${want}" >&2
		rm -f -- "$file"
		return 1
	fi
}

install_kind() {
	local bin
	bin="$(mktemp "${TMPDIR:-/tmp}/kind.XXXXXX")"
	# shellcheck disable=SC2064
	trap "rm -f -- $(printf '%q' "$bin")" RETURN
	fetch "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-${ARCH}" "$bin"
	verify_sha256 "$bin" "$(kind_sha256 "$ARCH")"
	sudo install -m 0755 "$bin" /usr/local/bin/kind
}

install_kubectl() {
	local bin
	bin="$(mktemp "${TMPDIR:-/tmp}/kubectl.XXXXXX")"
	# shellcheck disable=SC2064
	trap "rm -f -- $(printf '%q' "$bin")" RETURN
	fetch "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${ARCH}/kubectl" "$bin"
	verify_sha256 "$bin" "$(kubectl_sha256 "$ARCH")"
	sudo install -m 0755 "$bin" /usr/local/bin/kubectl
}

# Install the go.mod toolchain under /usr/local/go and link it ahead of the
# image's old /usr/bin/go, so `go run pkg@version` does not switch toolchains.
install_go() {
	local tgz extract
	tgz="$(mktemp "${TMPDIR:-/tmp}/go.XXXXXX")"
	extract="$(mktemp -d "${TMPDIR:-/tmp}/go-extract.XXXXXX")"
	# shellcheck disable=SC2064
	trap "rm -rf -- $(printf '%q' "$tgz") $(printf '%q' "$extract")" RETURN
	fetch "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" "$tgz"
	verify_sha256 "$tgz" "$(go_sha256 "$GO_VERSION" "$ARCH")"
	tar -C "$extract" -xzf "$tgz"
	# Replace only after a successful extract so a failed download cannot
	# leave /usr/local/go missing.
	sudo rm -rf /usr/local/go
	sudo mv "$extract/go" /usr/local/go
}

ARCH="$(host_arch)"
# Prefer the toolchain line; fall back to the go line.
GO_VERSION="$(awk '$1 == "toolchain" {sub(/^go/, "", $2); print $2; exit}' go.mod)"
if [[ -z "$GO_VERSION" ]]; then
	GO_VERSION="$(awk '$1 == "go" {print $2; exit}' go.mod)"
fi

apt_pkgs=()
command -v docker >/dev/null 2>&1 || apt_pkgs+=(docker.io docker-buildx)
command -v conntrack >/dev/null 2>&1 || apt_pkgs+=(conntrack)
command -v iptables >/dev/null 2>&1 || apt_pkgs+=(iptables)
command -v shellcheck >/dev/null 2>&1 || apt_pkgs+=(shellcheck)
command -v strace >/dev/null 2>&1 || apt_pkgs+=(strace)
if ((${#apt_pkgs[@]})); then
	./scripts/apt-install.sh "${apt_pkgs[@]}"
fi
sudo usermod -aG docker "$(id -un)"

if [[ "$(kind version 2>/dev/null | awk '{print $2}')" != "$KIND_VERSION" ]]; then
	install_kind
fi
if [[ "$(kubectl version --client -o json 2>/dev/null | sed -n 's/.*"gitVersion": *"\([^"]*\)".*/\1/p')" != "$KUBECTL_VERSION" ]]; then
	install_kubectl
fi
if [[ "$(GOTOOLCHAIN=local /usr/local/go/bin/go version 2>/dev/null | awk '{print $3}')" != "go${GO_VERSION}" ]]; then
	install_go
fi
sudo ln -sf /usr/local/go/bin/go /usr/local/bin/go
sudo ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
hash -r

# Every module and the CI lint tools, so make lint/test/build need no network
# inside the agent. Versions match the Makefile.
go mod download all
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 -version >/dev/null
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -version >/dev/null
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -version >/dev/null

go version
docker --version
kind version
kubectl version --client
