BINARY ?= bin/service
PKG ?= ./...

.PHONY: build test test-short lint fmt vet cover clean tidy

build:
	mkdir -p bin
	go build -ldflags '-s -w' -o $(BINARY) ./cmd/service

test:
	go test -race -v $(PKG)

test-short:
	go test -v -short $(PKG)

lint:
	golangci-lint run ./...

fmt:
	gofmt -s -w .
	goimports -w -local github.com/xiaotian/home-tutor-agent .

vet:
	go vet $(PKG)

# Coverage tracked per D-30; enforced-check threshold intentionally omitted for Phase 1.
cover:
	go test -race -coverprofile=coverage.out $(PKG)
	go tool cover -html=coverage.out -o coverage.html

clean:
	rm -rf bin/ coverage.out coverage.html

tidy:
	go mod tidy
