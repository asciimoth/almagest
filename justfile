set shell := ["bash", "-euo", "pipefail", "-c"]
set dotenv-load := true


check: tidy typos fmt lint shellcheck vet vulncheck test-total

typos:
  typos

test-go: certcat-ensure-generated
  ./scripts/test-go-coverage.sh
  ./scripts/test-gonnect-netstack-regressions.sh

# Run until Ctrl+C; keep failure logs under artifacts/test-cycles/.
test-cycles:
  @python3 ./scripts/test-cycles.py

test-cycles-harness:
  python3 -m unittest discover -s scripts/tests -p 'test_*.py'

certcat-test: certcat-ensure-generated
  ./scripts/test-go-coverage.sh certcat

test-js:
  cd panel && npm ci && npm test


[parallel]
test-e2e: test-e2e-docker test-winvm-enabled

[private]
test-e2e-docker:
  ./scripts/test-e2e-platform.sh

[parallel]
test-e2e-all: test-e2e-docker-all test-winvm-enabled

[private]
test-e2e-docker-all:
  E2E_INCLUDE_PRIVILEGED=1 ./scripts/test-e2e-platform.sh

test-e2e-light:
  E2E_EXCLUDE_HEAVY=1 just test-e2e

test: test-go test-js

test-total: test-cycles-harness test-winvm-harness test test-e2e
  ./scripts/coverage-report.sh

test-total-light: test test-e2e-light
  ./scripts/coverage-report.sh

coverage: test-total

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

tidy: repair-go-work
	go -C lib mod tidy
	go -C bin mod tidy
	go -C certcat mod tidy
	go work sync

repair-go-work:
	@if [ ! -f go.work ]; then go work init; fi
	go work use ./lib ./bin ./certcat

certcat-tidy:
	go -C certcat mod tidy

lint: test-winvm-harness certcat-lint lint-windows
  golangci-lint run ./lib/... ./bin/...

lint-windows:
  ./scripts/lint-windows.sh

shellcheck:
  git ls-files -z '*.sh' | xargs -0 shellcheck

certcat-lint: certcat-ensure-generated
  golangci-lint run ./certcat/...

fmt: certcat-fmt
  golangci-lint fmt ./lib/... ./bin/...
  git ls-files -z -- 'lib/*.go' 'lib/**/*.go' 'bin/*.go' 'bin/**/*.go' | xargs -0 -r gofmt -w

certcat-fmt: certcat-ensure-generated
  golangci-lint fmt ./certcat/...
  git ls-files -z -- 'certcat/*.go' 'certcat/**/*.go' | xargs -0 -r gofmt -w

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

build-windows:
  ./scripts/build-windows.sh amd64 arm64

build-windows-arch arch:
  ./scripts/build-windows.sh {{arch}}

test-winvm-harness:
  python3 -m unittest discover -s dev/winvm/tests -p 'test_*.py'
  shellcheck dev/winvm/*.sh scripts/lint-windows.sh scripts/test-e2e-platform.sh scripts/test-windows.sh scripts/test-windows-flow.sh

winvm-doctor:
  ./dev/winvm/doctor.sh

winvm-image:
  ./dev/winvm/build-image.sh

winvm-smoke:
  ./dev/winvm/run.sh smoke

winvm-test:
  ./dev/winvm/run.sh test

# Run the standard-user native Windows build and test gate. On a non-Windows
# host, use the locked disposable VM.
test-windows-vm:
  ./scripts/test-windows.sh ordinary

# Run serialized elevated backend and daemon integration tests. On a
# non-Windows host, use the locked disposable VM.
test-windows-e2e:
  ./scripts/test-windows.sh privileged

# Validate a retained isolated-flow evidence directory.
test-windows-flow evidence='':
  ./scripts/test-windows-flow.sh {{quote(evidence)}}

# Run the fixed 30-minute resource and recovery qualification gate.
# ALMAGEST_WINDOWS_SOAK_DURATION shortens a diagnostic run only.
test-windows-resource:
  ./dev/winvm/run.sh resource

# Validate captures made by an isolated two-link Windows run.
winvm-validate-flow observations tunnel underlay output:
  python3 ./dev/winvm/tools/flow-pcap.py --observations {{quote(observations)}} --tunnel {{quote(tunnel)}} --underlay {{quote(underlay)}} --output {{quote(output)}}

[private]
test-winvm-enabled:
  @if [[ "${RUN_WINVM_TESTS:-0}" == "1" ]]; then \
    just winvm-test; \
  else \
    printf 'Skipping native Windows VM tests; set RUN_WINVM_TESTS=1 to enable them.\n'; \
  fi

qualify-windows entry evidence:
  go run ./bin/cmd/windows-qualify -entry {{quote(entry)}} -evidence {{quote(evidence)}}

winvm-shell run:
  ./dev/winvm/run.sh --shell {{quote(run)}}

build: certcat-generate build-web
	go build -o ./almagestd ./bin/almagestd/

daemon: build
  ./almagestd -c ./local -l debug -w 8090 --debug-nodes --experimental --unsafe-config --tmp-unix-admin --no-dns-cache-expiration

daemon-sudo: build
  sudo ./almagestd -c ./local -l debug -w 8090 --debug-nodes --experimental --unsafe-config --no-dns-cache-expiration

capture-default-tun-flake:
  ./scripts/capture-default-tun-flake.sh

cli *args:
  go build -o ./cli ./bin/almagest-cli
  ./cli {{args}}

cli-sudo *args:
  go build -o ./cli ./bin/almagest-cli
  sudo ./cli {{args}}
