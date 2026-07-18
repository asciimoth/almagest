set shell := ["bash", "-euo", "pipefail", "-c"]
set dotenv-load := true

test-go:
	go -C lib test ./... --race
	go -C bin test ./... --race

test-js:
  cd panel && npm ci && npm test


test-e2e:
  ./e2e/run.sh

test: test-go test-js

test-total: test test-e2e

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
  cd panel && npm ci && npm run build

build: build-web
	go build -o ./almagestd ./bin/almagestd/

daemon: build
  ./almagestd -c ./local -l debug -w 8090 --debug-nodes --unsafe-config --tmp-unix-admin

daemon-sudo: build
  sudo ./almagestd -c ./local -l debug -w 8090 --debug-nodes --unsafe-config

cli *args:
  go build -o ./cli ./bin/almagest-cli
  ./cli {{args}}
