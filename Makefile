SHELL := bash
.SHELLFLAGS := -euo pipefail -c

MODULE := github.com/groundcover-com/kray
# The product name lives in one constant: internal/brand (const Name).
# The fallback is only for the time before that file exists (M1).
NAME := $(or $(shell sed -n 's/^const Name = "\(.*\)"$$/\1/p' internal/brand/brand.go 2>/dev/null),kray)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/brand.Version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

# Pinned tool versions (go run fetches them; no global installs).
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0
ACTIONLINT := github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
GOTESTSUM := gotest.tools/gotestsum@v1.13.0

.PHONY: fmt vet staticcheck vulncheck lint tidy tidy-check shellcheck actionlint \
	test test-race test-junit build install build-all dist \
	kind-up kind-down integration harness bench bench-hard bench-up bench-agents bench-down clean

fmt:
	@files="$$(git ls-files '*.go')"; \
	if [ -n "$$files" ]; then \
		out="$$(gofmt -s -l -- $$files)"; \
		if [ -n "$$out" ]; then echo "These files need gofmt -s:"; echo "$$out"; exit 1; fi; \
	fi

vet:
	go vet ./...

staticcheck:
	go run $(STATICCHECK) ./...

vulncheck:
	go run $(GOVULNCHECK) ./...

lint: fmt vet staticcheck vulncheck

tidy:
	go mod tidy

tidy-check:
	go mod tidy
	git diff --exit-code -- go.mod $(wildcard go.sum)

shellcheck:
	shellcheck scripts/*.sh

actionlint:
	go run $(ACTIONLINT)

test:
	go test ./...

test-race:
	go test -race -count=1 ./...

# CI: JUnit for the trace export (artifact name test-reports-<job>).
test-junit:
	mkdir -p reports
	go run $(GOTESTSUM) --junitfile reports/test-junit.xml -- -race -count=1 ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(NAME) .

# Install the binary for one person: make install (to ~/.local/bin) or
# make install PREFIX=/usr/local.
PREFIX ?= $(HOME)/.local
install:
	mkdir -p $(PREFIX)/bin
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(PREFIX)/bin/$(NAME) .
	@echo "installed $(PREFIX)/bin/$(NAME)"

build-all:
	mkdir -p dist
	for p in $(PLATFORMS); do \
		os="$${p%/*}"; arch="$${p#*/}"; ext=""; [ "$$os" = windows ] && ext=".exe"; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS="$$os" GOARCH="$$arch" go build -trimpath -ldflags '$(LDFLAGS)' \
			-o "dist/$(NAME)_$(VERSION)_$${os}_$${arch}$${ext}" . ; \
	done

dist: build-all
	cd dist && if command -v sha256sum >/dev/null; then sha256sum -- $(NAME)_* > SHA256SUMS; \
		else shasum -a 256 -- $(NAME)_* > SHA256SUMS; fi

# Integration (M2): a kind cluster with API server audit logging and
# metrics-server, the canary fixture, and the harness. Needs Docker.
kind-up:
	./scripts/kind.sh up

kind-down:
	./scripts/kind.sh down

integration:
	go test -tags integration -count=1 -timeout 30m ./test/integration/...

# Point the canary harness at any MCP server command:
#   make harness TARGET="npx -y some-mcp-server"
harness:
	@if [ -z "$(TARGET)" ]; then echo 'usage: make harness TARGET="<mcp server command>"'; exit 2; fi
	go test -tags integration -count=1 -timeout 30m -run TestCanaryHarness ./test/integration/... -args -target='$(TARGET)'

# Output size against kubectl on the kind fixture (M6). Needs kubectl.
bench:
	go run ./bench -kubeconfig test/integration/out/kubeconfig -out bench/RESULTS.md

# Free size test for the hard agent benchmarks (no LLM). Needs kubectl and the
# fixtures applied: go run ./bench/agents -apply -scenarios bench/agents/hard/tokens
bench-hard:
	go run ./bench/hard -kubeconfig test/integration/out/kubeconfig -out bench/HARD_RESULTS.md

# Reproduce the agent benchmark (docs/benchmarks.md). Needs Docker, kind,
# kubectl, and the claude CLI with an account; the agent runs cost money.
#   make bench-up                        # cluster kray-bench and the faults of BENCH_SET
#   make bench-agents                    # the agents; results in BENCH_OUT
#   make bench-down                      # delete the cluster
# BENCH_SET is a directory under bench/agents: heldout2 (the blind set),
# heldout, heldout2-session (a conversation), hard/tokens or hard/secrets.
BENCH_SET ?= heldout2
BENCH_RUNS ?= 3
BENCH_MODEL ?= sonnet
BENCH_ARMS ?= kubectl,cli,mcp
BENCH_OUT ?= bench/agents/results/local-$(subst /,-,$(BENCH_SET))

bench-up:
	KIND_CLUSTER_NAME=kray-bench ./scripts/kind.sh up
	KIND_CLUSTER_NAME=kray-bench ./scripts/kind.sh metrics-server
	go run ./bench/agents -apply -scenarios bench/agents/$(BENCH_SET)

bench-agents:
	go run ./bench/agents -lean -runs $(BENCH_RUNS) -model $(BENCH_MODEL) \
		-scenarios bench/agents/$(BENCH_SET) -arms $(BENCH_ARMS) -out $(BENCH_OUT)

bench-down:
	KIND_CLUSTER_NAME=kray-bench ./scripts/kind.sh down

clean:
	rm -rf bin dist reports
