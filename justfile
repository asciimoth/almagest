set shell := ["bash", "-euo", "pipefail", "-c"]
set dotenv-load := true


check *selectors: tidy typos fmt lint shellcheck vet vulncheck
  @just test-total {{quote(selectors)}}

typos:
  typos

test-go: certcat-ensure-generated build-web
  ./scripts/test-go-coverage.sh
  ./scripts/test-gonnect-regressions.sh
  ./scripts/test-gonnect-netstack-regressions.sh

# Run until stopped or an optional timeout expires; keep failure logs under artifacts/test-cycles/.
test-cycles:
  @python3 ./scripts/test-cycles.py

test-cycles-harness:
  python3 -m unittest discover -s scripts/tests -p 'test_*.py'

certcat-test: certcat-ensure-generated
  ./scripts/test-go-coverage.sh certcat

test-js: build-web
  cd panel && node --test tests/*.mjs


test-e2e:
  ./scripts/test-e2e-fail-fast.sh

[private]
test-e2e-docker: repair-go-work build-web
  ./scripts/test-e2e-platform.sh

test-e2e-all:
  ./scripts/test-e2e-fail-fast.sh test-e2e-docker-all test-winvm-enabled

# Run the complete direct-mode qualification gate and its retained allocation
# benchmark. Optional platform suites must run; a skip is not qualification.
qualify-direct:
  RUN_WINVM_TESTS=1 E2E_INCLUDE_PRIVILEGED=1 just check
  go -C lib test ./tun -run '^$' -bench '^BenchmarkAutoTunSplitterRouter$' -benchmem -benchtime=500ms -count=3

[private]
test-e2e-docker-all: repair-go-work build-web
  E2E_INCLUDE_PRIVILEGED=1 ./scripts/test-e2e-platform.sh

test-e2e-light:
  E2E_EXCLUDE_HEAVY=1 just test-e2e

test: test-go test-js

test-total *selectors:
  @if [[ -n {{quote(selectors)}} ]]; then \
    python3 ./scripts/test-selected.py {{quote(selectors)}}; \
  else \
    just test-total-all; \
  fi

[private]
test-total-all: test-cycles-harness test-winvm-harness test test-e2e
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
	go work edit -go=1.26.6

certcat-tidy:
	go -C certcat mod tidy

lint: test-winvm-harness certcat-lint lint-windows lint-winvm lint-secrets
  cd lib && golangci-lint run ./...
  cd bin && golangci-lint run ./...

lint-secrets:
  ripsecrets

lint-windows: build-web
  ./scripts/lint-windows.sh

shellcheck:
  @if [[ "${RUN_WINVM_TESTS:-0}" == "1" ]]; then \
    git ls-files -z '*.sh' | xargs -0 shellcheck; \
  else \
    git ls-files -z '*.sh' ':!dev/winvm/**' ':!scripts/lint-windows.sh' ':!scripts/test-e2e-platform.sh' ':!scripts/test-windows.sh' ':!scripts/test-windows-flow.sh' | xargs -0 shellcheck; \
  fi

certcat-lint: certcat-ensure-generated
  cd certcat && golangci-lint run ./...

fmt: certcat-fmt
  cd lib && golangci-lint fmt ./...
  cd bin && golangci-lint fmt ./...
  git ls-files -z -- 'lib/*.go' 'lib/**/*.go' 'bin/*.go' 'bin/**/*.go' | xargs -0 -r gofmt -w

certcat-fmt: certcat-ensure-generated
  cd certcat && golangci-lint fmt ./...
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
  @if [[ "${RUN_WINVM_TESTS:-0}" == "1" ]]; then \
    python3 -m unittest discover -s dev/winvm/tests -p 'test_*.py'; \
  else \
    printf 'Skipping Windows VM harness tests; set RUN_WINVM_TESTS=1 to enable them.\n'; \
  fi

lint-winvm:
  @if [[ "${RUN_WINVM_TESTS:-0}" == "1" ]]; then \
    shellcheck dev/winvm/*.sh scripts/lint-windows.sh scripts/test-e2e-platform.sh scripts/test-windows.sh scripts/test-windows-flow.sh; \
    ruff check --select E4,E9,F dev/winvm; \
    pwsh -NoLogo -NoProfile -NonInteractive -Command '& { Import-Module $env:PSSCRIPTANALYZER_PATH; $issues = @(Invoke-ScriptAnalyzer -Path ./dev/winvm -Recurse -Severity Warning,Error); $issues | Format-Table -AutoSize; if ($issues.Count) { exit 1 } }'; \
  else \
    printf 'Skipping Windows VM lint; set RUN_WINVM_TESTS=1 to enable it.\n'; \
  fi

winvm-doctor:
  ./dev/winvm/doctor.sh

winvm-image:
  ./dev/winvm/build-image.sh

winvm-smoke:
  ./dev/winvm/run.sh smoke

winvm-test:
  ./dev/winvm/run.sh test
  ./dev/winvm/flow-run.sh

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

# Run an elevated daemon in a visible Windows VM. Stop the VM with Ctrl+C or by
# closing its QEMU window.
daemon-winvm web_port='8091':
  WINVM_WEB_PORT={{quote(web_port)}} WINVM_OPEN_BROWSER="${WINVM_OPEN_BROWSER:-0}" ./dev/winvm/run.sh manual

build: certcat-generate build-web
	go build -o ./almagestd ./bin/almagestd/

daemon: build
  ./almagestd --no-isolation -c ./local -l debug -w 8090 --debug-nodes --experimental --unsafe-config --tmp-unix-admin --no-dns-cache-expiration

daemon-sudo: build
  sudo ./almagestd --no-isolation -c ./local -l debug -w 8090 --debug-nodes --experimental --unsafe-config --no-dns-cache-expiration

capture-default-tun-flake:
  ./scripts/capture-default-tun-flake.sh

cli *args:
  go build -o ./cli ./bin/almagest-cli
  ./cli {{args}}

cli-sudo *args:
  go build -o ./cli ./bin/almagest-cli
  sudo ./cli {{args}}
