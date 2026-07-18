.PHONY: build run test lint cover tidy clean
BIN ?= bin/kapctl
PKG := ./...
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

build:
	@mkdir -p $(dir $(BIN))
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/kapctl

run: build
	$(BIN)

test:
	go test $(PKG)

lint:
	golangci-lint run

cover:
	go test -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -n 1

tidy:
	go mod tidy

clean:
	rm -rf bin/ coverage.out coverage.html
