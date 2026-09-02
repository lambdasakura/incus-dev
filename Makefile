BIN     := bin/idev
PKG     := ./cmd/idev
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
COVER   := cover.out

# Unit tests must pass where no Incus is reachable (spec 08-testing.md 8.4.1).
# Pointing the client at a socket that does not exist stops a daemon running on
# the developer's machine from hiding a test that needs one.
NO_INCUS := INCUS_SOCKET=/nonexistent/incus.socket

# One place for the linter version, so make lint, GitHub CI and GitLab CI
# cannot end up running three different linters.
LINT_VERSION := $(shell cat .golangci-lint-version)

.PHONY: build test test-integration cover cover-html lint fmt check tidy clean install tools

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

test:
	$(NO_INCUS) go test ./...

test-integration:
	go test -tags integration -count=1 -timeout 20m ./test/integration/...

# Coverage is measured across packages. internal/incus/incustest, for one, runs
# only from the tests of other packages, so without -coverpkg it is counted as
# 0%.
cover:
	$(NO_INCUS) go test ./... -coverpkg=./... -coverprofile=$(COVER)
	@go tool cover -func=$(COVER) | tail -1

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

tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VERSION)

# The checks CI runs on the source. CI additionally builds the release with
# goreleaser, which no target here covers.
check: tidy lint test

tidy:
	go mod tidy -diff

install:
	CGO_ENABLED=0 go install -ldflags "$(LDFLAGS)" $(PKG)

clean:
	rm -rf bin dist $(COVER)
