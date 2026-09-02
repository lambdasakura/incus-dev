BIN     := bin/idev
PKG     := ./cmd/idev
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
COVER   := cover.out

.PHONY: build test test-integration cover cover-html lint fmt check clean install tools

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

test:
	go test ./...

test-integration:
	go test -tags integration -count=1 -timeout 20m ./test/integration/...

# カバレッジはパッケージ横断で測る。
# 例えば internal/incus/incustest は他パッケージのテストからのみ実行されるため、
# -coverpkg を付けないと0%として集計されてしまう。
cover:
	go test ./... -coverpkg=./... -coverprofile=$(COVER)
	@go tool cover -func=$(COVER) | tail -1

cover-html: cover
	go tool cover -html=$(COVER)

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint が無いため gofmt / go vet で代替します（make tools で導入できます）"; \
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
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

check: lint test

install:
	CGO_ENABLED=0 go install -ldflags "$(LDFLAGS)" $(PKG)

clean:
	rm -rf bin $(COVER)
