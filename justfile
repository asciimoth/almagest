set shell := ["bash", "-euo", "pipefail", "-c"]
set dotenv-load := true

test-go:
	go -C lib test ./... --race -count=1
	go -C bin test ./... --race -count=1

test-js:
  # Placeholder
  # TODO: Run npm project tests

test: test-go

coverage:
	go -C lib test ./... --race -coverprofile=../coverage.out -coverpkg=./...

vet:
	go -C lib vet ./...
	go -C bin vet ./...

tidy:
	go -C lib mod tidy
	go -C bin mod tidy
	go work sync

lint:
  golangci-lint run ./lib/... ./bin/...

fmt:
  golangci-lint fmt ./lib/... ./bin/...

build-web:
  # Placeholder
  # TODO: Web admin static page building

daemon: build-web
  go run ./bin/almagestd/ -c ./local -l debug -w 8090

