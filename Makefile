BIN     := bin/idev
PKG     := ./cmd/idev
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test test-integration lint fmt check clean install

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

test:
	go test ./...

test-integration:
	go test -tags integration -count=1 -timeout 20m ./test/integration/...

lint:
	@out="$$(gofmt -l . )"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go vet ./...

fmt:
	gofmt -w .

check: lint test

install:
	CGO_ENABLED=0 go install -ldflags "$(LDFLAGS)" $(PKG)

clean:
	rm -rf bin
