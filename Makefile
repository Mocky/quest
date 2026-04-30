MODULE  := github.com/mocky/quest
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build install test test-all test-eval eval-compare cover lint ci

build:
	go build -ldflags "-X $(MODULE)/internal/buildinfo.Version=$(VERSION)" -o quest ./cmd/quest

install:
	go install -ldflags "-X $(MODULE)/internal/buildinfo.Version=$(VERSION)" ./cmd/quest

test:
	go test ./...

test-all:
	go test -race -tags integration -count=1 ./...

test-eval:
	go test -tags eval -count=1 -v ./internal/eval/...

eval-compare:
	go run ./cmd/eval-compare/

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

lint:
	go vet ./...
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt: files need reformatting:"; echo "$$out"; exit 1; fi

ci:
	go test -race -count=1 -tags integration -coverprofile=coverage.out ./...
