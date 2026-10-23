set shell := ["./scripts/run-recipe.sh", "bash", "-euo", "pipefail", "-c"]
set dotenv-load := true


# TODO: Enable vulncheck again after Go is updated to 1.26.9 or later.
check *selectors: tidy typos fmt lint actionlint shellcheck vet # vulncheck
  @just locales-check
  @just test-total {{quote(selectors)}}
  @if [[ -z {{quote(selectors)}} ]]; then just fuzz; fi

typos:
  typos

test-go: certcat-ensure-generated build-web
  bash ./scripts/with-test-tmp.sh ./scripts/test-go-coverage.sh
  bash ./scripts/with-test-tmp.sh ./scripts/test-gonnect-regressions.sh
  bash ./scripts/with-test-tmp.sh ./scripts/test-gonnect-netstack-regressions.sh

# Run until stopped or an optional timeout expires; keep failure logs under artifacts/test-cycles/.
test-cycles:
  @python3 ./scripts/test-cycles.py

test-cycles-harness:
  bash ./scripts/with-test-tmp.sh python3 -m unittest discover -s scripts/tests -p 'test_*.py'

certcat-test: certcat-ensure-generated
  bash ./scripts/with-test-tmp.sh ./scripts/test-go-coverage.sh certcat

test-js: build-web
  cd panel && node --test tests/*.mjs


test-e2e:
  bash ./scripts/with-test-tmp.sh ./scripts/test-e2e-fail-fast.sh

[private]
test-e2e-docker: repair-go-work build-web
  bash ./scripts/with-test-tmp.sh ./scripts/test-e2e-platform.sh

test-e2e-all:
  bash ./scripts/with-test-tmp.sh ./scripts/test-e2e-fail-fast.sh test-e2e-docker-all test-winvm-enabled

# Run the complete direct-mode qualification gate and its retained allocation
# benchmark. Optional platform suites must run; a skip is not qualification.
qualify-direct:
  RUN_WINVM_TESTS=1 E2E_INCLUDE_PRIVILEGED=1 just check
  go -C lib test ./tun -run '^$' -bench '^BenchmarkAutoTunSplitterRouter$' -benchmem -benchtime=500ms -count=3

# Run the production Linux isolation profile and its backend compatibility
# cases through the normal E2E harness.
linux-isolation-profiles:
  just test-total e2e:linux_isolation_profile e2e:linux_adaptive_isolation e2e:sysnet_dns_debian_resolvconf e2e:sysnet_dns_openresolv e2e:sysnet_dns_systemd_resolved e2e:tun_splitter_process_routing

# Check Windows role restrictions in both identities in the disposable VM.
windows-isolation-profiles:
  just test-total winvm:m25::windows-role-profiles-caller winvm:m25::windows-role-profiles-service

[private]
test-e2e-docker-all: repair-go-work build-web
  E2E_INCLUDE_PRIVILEGED=1 bash ./scripts/with-test-tmp.sh ./scripts/test-e2e-platform.sh

test-e2e-light:
  E2E_EXCLUDE_HEAVY=1 just test-e2e

test: test-go test-js

# Run all Go fuzz targets with one shared time budget.
fuzz: repair-go-work certcat-ensure-generated build-web
  bash ./scripts/with-test-tmp.sh ./scripts/test-fuzz.py

test-total *selectors:
  @if [[ -n {{quote(selectors)}} ]]; then \
    bash ./scripts/with-test-tmp.sh python3 ./scripts/test-selected.py {{quote(selectors)}}; \
  else \
    bash ./scripts/with-test-tmp.sh just test-total-all; \
  fi

[private]
test-total-all: test-cycles-harness test-winvm-harness
  ./scripts/test-e2e-fail-fast.sh test test-e2e-docker test-winvm-enabled
  ./scripts/coverage-report.sh

test-total-light:
  bash ./scripts/with-test-tmp.sh ./scripts/test-e2e-fail-fast.sh test test-e2e-light
  ./scripts/coverage-report.sh

coverage: test-total

vet: certcat-vet
	go -C lib vet ./...
	go -C bin vet ./...

certcat-vet: certcat-ensure-generated
	go -C certcat vet ./...

locales-check:
	go -C bin run ./cmd/locales-check

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

lint: test-winvm-harness certcat-lint lint-windows lint-winvm lint-secrets markdownlint
  cd lib && golangci-lint run ./...
  cd bin && golangci-lint run ./...

markdownlint:
  git ls-files -z -- '*.md' ':!locales/lorem/docs/pages/guides/markdown-features.md' | xargs -0 -r markdownlint

lint-secrets:
  ripsecrets

actionlint:
  actionlint

lint-windows: build-web
  ./scripts/lint-windows.sh

shellcheck:
  @if [[ "${RUN_WINVM_TESTS:-0}" == "1" ]]; then \
    git ls-files -z '*.sh' | while IFS= read -r -d '' file; do [[ -f "$file" ]] && printf '%s\0' "$file"; done | xargs -0 -r shellcheck; \
  else \
    git ls-files -z '*.sh' ':!dev/winvm/**' ':!scripts/lint-windows.sh' ':!scripts/test-e2e-platform.sh' ':!scripts/test-windows.sh' ':!scripts/test-windows-flow.sh' | while IFS= read -r -d '' file; do [[ -f "$file" ]] && printf '%s\0' "$file"; done | xargs -0 -r shellcheck; \
  fi

certcat-lint: certcat-ensure-generated
  cd certcat && golangci-lint run ./...

fmt: certcat-fmt markdown-fmt
  cd lib && golangci-lint fmt ./...
  cd bin && golangci-lint fmt ./...
  git ls-files -z -- 'lib/*.go' 'lib/**/*.go' 'bin/*.go' 'bin/**/*.go' | while IFS= read -r -d '' file; do [[ -f "$file" ]] && printf '%s\0' "$file"; done | xargs -0 -r gofmt -w

markdown-fmt:
  git ls-files -z -- '*.md' ':!locales/lorem/docs/pages/guides/markdown-features.md' | xargs -0 -r mdformat --wrap 80

certcat-fmt: certcat-ensure-generated
  cd certcat && golangci-lint fmt ./...
  git ls-files -z -- 'certcat/*.go' 'certcat/**/*.go' | while IFS= read -r -d '' file; do [[ -f "$file" ]] && printf '%s\0' "$file"; done | xargs -0 -r gofmt -w

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
	rm -f ./almagestd ./cli ./almagest-subprogram-launcher

build-web:
  cd panel && npm ci && npm run build

build-windows:
  ./scripts/build-windows.sh amd64 arm64

# Build the complete Linux/amd64 isolated service payload. Packaging must
# install this set atomically in one root-owned directory.
build-isolation-linux output='build/isolation-linux': build-web
  mkdir -p {{quote(output)}}
  mkdir -p {{quote(output)}}/share/almagest/examples
  cp -a examples/. {{quote(output)}}/share/almagest/examples/
  CGO_ENABLED=0 go build -C bin -trimpath -o ../{{quote(output)}}/almagestd ./almagestd
  CGO_ENABLED=0 go build -C bin -trimpath -o ../{{quote(output)}}/almagest-network-role ./cmd/almagest-network-role
  CGO_ENABLED=0 go build -C bin -trimpath -o ../{{quote(output)}}/almagest-config-writer-role ./cmd/almagest-config-writer-role
  CGO_ENABLED=0 go build -C bin -trimpath -o ../{{quote(output)}}/almagest-command-role ./cmd/almagest-command-role
  CGO_ENABLED=0 go build -C bin -trimpath -o ../{{quote(output)}}/almagest-isolation-storage ./cmd/almagest-isolation-storage
  gcc -static-pie -O2 -D_FORTIFY_SOURCE=3 -fstack-protector-strong -fstack-clash-protection -fno-strict-overflow -Wformat=2 -Wall -Wextra -Werror -Wl,-z,relro,-z,now,-z,noexecstack -o {{quote(output)}}/almagest-linux-role-launcher bin/native/linux-role/launcher.c $(pkg-config --cflags --libs libseccomp)
  gcc -static-pie -O2 -D_FORTIFY_SOURCE=3 -fstack-protector-strong -fstack-clash-protection -fno-strict-overflow -Wformat=2 -Wall -Wextra -Werror -Wl,-z,relro,-z,now,-z,noexecstack -o {{quote(output)}}/almagest-subprogram-launcher bin/native/subprogram/launcher.c $(pkg-config --cflags --libs libseccomp)

build-windows-arch arch:
  ./scripts/build-windows.sh {{arch}}

test-winvm-harness:
  @if [[ "${RUN_WINVM_TESTS:-0}" == "1" ]]; then \
    bash ./scripts/with-test-tmp.sh python3 -m unittest discover -s dev/winvm/tests -p 'test_*.py'; \
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

# Run an elevated daemon in a visible Windows VM. Windows production isolation
# is not available yet, so this recipe stays in explicit direct mode. Pass
# --experimental to enable experimental nodes. Set WINVM_WEB_PORT to override
# the host port. Stop the VM with Ctrl+C or by closing its QEMU window.
daemon-winvm *flags:
  @options={{quote(flags)}}; \
  for option in $options; do \
    case "$option" in --no-isolation|--experimental) ;; *) printf 'unsupported daemon-winvm flag: %s\n' "$option" >&2; exit 2;; esac; \
  done; \
  WINVM_WEB_PORT="${WINVM_WEB_PORT:-8091}" WINVM_DAEMON_OPTIONS="--discover-system-programs $options" WINVM_OPEN_BROWSER="${WINVM_OPEN_BROWSER:-0}" ./dev/winvm/run.sh manual

build: certcat-generate build-web
	go build -o ./almagestd ./bin/almagestd/
	gcc -static-pie -O2 -D_FORTIFY_SOURCE=3 -fstack-protector-strong -fstack-clash-protection -fno-strict-overflow -Wformat=2 -Wall -Wextra -Werror -Wl,-z,relro,-z,now,-z,noexecstack -o ./almagest-subprogram-launcher bin/native/subprogram/launcher.c $(pkg-config --cflags --libs libseccomp)

# Run the daemon directly with local development config. Pass --experimental
# to enable experimental nodes. Use -l or --lvl to set the log level.
daemon *flags: build
  ./scripts/run-daemon.sh {{quote(flags)}}

# Run the daemon as root in isolated mode with local config. The runner stages
# role-owned state and copies mutable config back after the daemon stops. Use
# -l or --lvl to set the log level.
daemon-sudo *flags: build-isolation-linux
  ./scripts/run-daemon.sh --sudo {{quote(flags)}}

capture-default-tun-flake:
  ./scripts/capture-default-tun-flake.sh

cli *args:
  go build -o ./cli ./bin/almagest-cli
  ./cli {{args}}

cli-sudo *args:
  go build -o ./cli ./bin/almagest-cli
  sudo ./cli {{args}}
