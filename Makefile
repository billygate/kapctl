.PHONY: build run test lint cover tidy clean
BIN ?= bin/kapctl
PKG := ./...

build:
	@mkdir -p $(dir $(BIN))
	go build -o $(BIN) ./cmd/kapctl

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
