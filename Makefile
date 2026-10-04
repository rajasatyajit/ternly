VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
BIN     ?= $(HOME)/.local/bin/ternly

install:            ## build a static, stripped binary into ~/.local/bin
	go mod tidy
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BIN) .
	@echo "installed $(BIN)"

test:
	go vet ./... && go test -race ./...

.PHONY: install test
