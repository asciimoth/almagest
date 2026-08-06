set shell := ["bash", "-euo", "pipefail", "-c"]
set dotenv-load := true


check: tidy typos fmt lint vet vulncheck test-total

typos:
  typos

test-go:
	go -C lib test ./... --race
	go -C bin test ./... --race

certcat-test: certcat-ensure-generated
	go -C certcat test ./...

test-js:
  cd panel && npm ci && npm test


test-e2e:
  ./e2e/run.sh

test: certcat-test test-go test-js

test-total: test test-e2e

coverage:
	go -C lib test ./... --race -coverprofile=../coverage.out -coverpkg=./...

vet: certcat-vet
	go -C lib vet ./...
	go -C bin vet ./...

certcat-vet: certcat-ensure-generated
	go -C certcat vet ./...

vulncheck: certcat-vulncheck
	cd lib && govulncheck ./...
	cd bin && govulncheck ./...

certcat-vulncheck: certcat-ensure-generated
	cd certcat && govulncheck ./...

tidy:
	go -C lib mod tidy
	go -C bin mod tidy
	go -C certcat mod tidy
	go work sync

certcat-tidy:
	go -C certcat mod tidy

lint: certcat-lint
  golangci-lint run ./lib/... ./bin/...

certcat-lint: certcat-ensure-generated
  golangci-lint run ./certcat/...

fmt: certcat-fmt
  golangci-lint fmt ./lib/... ./bin/...

certcat-fmt: certcat-ensure-generated
  golangci-lint fmt ./certcat/...

certcat-fetch:
	@if [ -f certcat/data/machine.json ]; then \
	  printf 'certcat/data/machine.json already exists; skipping fetch\n'; \
	else \
	  just certcat-fetch-force; \
	fi

certcat-fetch-force:
	@for n in 1 2 3; do \
	  out="$(go -C certcat run ./cmd/certcat-fetch -out data/machine.json 2>&1)" && { printf '%s\n' "$out"; exit 0; }; \
	  if [ "$n" = 3 ]; then printf '%s\n' "$out"; exit 1; fi; \
	  printf 'certcat fetch attempt %s failed; retrying\n' "$n" >&2; \
	  sleep "$((n * 2))"; \
	done

certcat-generate: certcat-fetch
	go -C certcat run ./cmd/certcat-generate -manual data/manual.json -machine data/machine.json -out catalog_gen.go

certcat-ensure-generated:
	@just certcat-fetch
	go -C certcat run ./cmd/certcat-generate -manual data/manual.json -machine data/machine.json -out catalog_gen.go

certcat-clean:
	rm -f ./certcat/data/machine.json ./certcat/catalog_gen.go

clean: certcat-clean
	rm -f ./almagestd ./cli

build-web:
  cd panel && npm ci && npm run build

build: certcat-generate build-web
	go build -o ./almagestd ./bin/almagestd/

daemon: build
  ./almagestd -c ./local -l debug -w 8090 --debug-nodes --unsafe-config --tmp-unix-admin --no-dns-cache-expiration

daemon-sudo: build
  sudo ./almagestd -c ./local -l debug -w 8090 --debug-nodes --unsafe-config --no-dns-cache-expiration

cli *args:
  go build -o ./cli ./bin/almagest-cli
  ./cli {{args}}
