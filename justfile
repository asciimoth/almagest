set shell := ["bash", "-euo", "pipefail", "-c"]
set dotenv-load := true


check: tidy typos fmt lint shellcheck vet vulncheck test-total

typos:
  typos

test-go: certcat-ensure-generated
  ./scripts/test-go-coverage.sh
  ./scripts/test-gonnect-netstack-regressions.sh

certcat-test: certcat-ensure-generated
  ./scripts/test-go-coverage.sh certcat

test-js:
  cd panel && npm ci && npm test


test-e2e:
  ./e2e/lib/case_scheduler_test.sh
  ./e2e/lib/common_test.sh
  ./e2e/lib/amnesia_peer_handoff_test.sh
  python3 ./e2e/lib/udp_echo_client_test.py
  E2E_COVERAGE=1 ./e2e/run.sh

test-e2e-light:
  E2E_EXCLUDE_HEAVY=1 just test-e2e

test: test-go test-js

test-total: test-winvm-harness test test-e2e
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

lint: test-winvm-harness certcat-lint
  golangci-lint run ./lib/... ./bin/...

shellcheck:
  git ls-files -z '*.sh' | xargs -0 shellcheck

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

build-windows:
  ./scripts/build-windows.sh amd64 arm64

build-windows-arch arch:
  ./scripts/build-windows.sh {{arch}}

test-winvm-harness:
  python3 -m unittest discover -s dev/winvm/tests -p 'test_*.py'
  shellcheck dev/winvm/*.sh

winvm-doctor:
  ./dev/winvm/doctor.sh

winvm-image:
  ./dev/winvm/build-image.sh

winvm-smoke:
  ./dev/winvm/run.sh smoke

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
