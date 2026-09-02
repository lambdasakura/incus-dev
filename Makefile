BIN     := bin/idev
PKG     := ./cmd/idev
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
COVER   := cover.out
VULN    := vuln.json

# Unit tests must pass where no Incus is reachable (spec 08-testing.md 8.4.1).
# Pointing the client at a socket that does not exist stops a daemon running on
# the developer's machine from hiding a test that needs one.
NO_INCUS := INCUS_SOCKET=/nonexistent/incus.socket

# One place for the linter version, so make lint, GitHub CI and GitLab CI
# cannot end up running three different linters.
LINT_VERSION = $(shell cat .golangci-lint-version)

.PHONY: build test test-integration cover cover-html lint strict-lint vuln fmt check tidy clean install tools

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

test:
	$(NO_INCUS) go test ./...

test-integration:
	go test -tags integration -count=1 -timeout 20m ./test/integration/...

# Coverage is measured across packages. internal/incus/incustest, for one, runs
# only from the tests of other packages, so without -coverpkg it is counted as
# 0%.
# -o rather than a pipe: `| tail -1` reports the exit code of tail, so a
# failure to read the profile came out as a pass with no total printed.
# The contract package is assertions, not product code: it is the same suite
# run against the fake and against the real daemon, so the daemon-only half
# never executes here (spec 08-testing.md 8.3.2).
# Recursive, not simple: a := would shell out to go list on every make
# invocation, including ones that never measure coverage.
COVERPKG = $(shell go list ./... | grep -v /internal/incus/contract | paste -sd,)

cover:
	$(NO_INCUS) go test ./... -coverpkg=$(COVERPKG) -coverprofile=$(COVER)
	@go tool cover -func=$(COVER) -o $(COVER).func
	@tail -1 $(COVER).func

cover-html: cover
	go tool cover -html=$(COVER)

# The fallback is weaker than it looks: go vet does not type-check
# test/integration/, which only .golangci.yml's build-tags reach. Say so, so a
# green run is not mistaken for the real thing.
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint is not installed, so this is gofmt / go vet only"; \
		echo "  it does not check test/integration/; run 'make tools' to install it"; \
		out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi; \
		go vet ./...; \
	fi

fmt:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint fmt ./...; \
	else \
		gofmt -w .; \
	fi

# govulncheck reports every advisory in a module that is linked in, and the
# Incus client shares packages with the daemon, so this binary "calls" a long
# list of server-side advisories that have no fix in any released version. A
# gate that can never be green is a gate that gets switched off.
#
# So this fails on the one thing a version bump can fix: a vulnerability this
# code calls whose fix is in the module already used. The rest are printed
# with where their fix does exist -- several are fixed only in incus/v7, which
# is a deliberate migration rather than a bump -- so the list says what it
# knows instead of a flat "no fix".
vuln:
	@if ! command -v jq >/dev/null 2>&1; then \
		echo "jq is required by 'make vuln'"; \
		exit 1; \
	fi
	@go run golang.org/x/vuln/cmd/govulncheck@latest -format json ./... > $(VULN)
	@jq -s -r -f scripts/vuln.jq $(VULN) > $(VULN).called
	@echo "vulnerabilities this code calls:"; cat $(VULN).called
	@if grep -q "	update to " $(VULN).called; then \
		echo; echo "the above have a fix in the module already used; update it"; exit 1; \
	fi

tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VERSION)

# The checks CI runs on the source. CI additionally builds the release with
# goreleaser, which no target here covers.
#
# strict-lint, not lint: a gate that says "check" must not pass on a machine
# where the linter is missing. It once did, and an unformatted file reached a
# commit for CI to find.
check: tidy strict-lint test

strict-lint:
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "golangci-lint is required by 'make check'; run 'make tools'"; \
		exit 1; \
	fi
	golangci-lint run ./...

tidy:
	go mod tidy -diff

install:
	CGO_ENABLED=0 go install -ldflags "$(LDFLAGS)" $(PKG)

clean:
	rm -rf bin dist $(COVER) $(COVER).func $(VULN) $(VULN).called
