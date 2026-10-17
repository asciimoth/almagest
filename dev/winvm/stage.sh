#!/usr/bin/env bash
set -euo pipefail
run_dir=${1:?usage: stage.sh RUN_DIRECTORY [MODE]}
mode=${2:-test}
mkdir "$run_dir/source"
tar -xf "$run_dir/source.tar" -C "$run_dir/source"
cd "$run_dir/source"
# All generator inputs come from the archived tree. Never use ignored host assets.
export GOWORK="$PWD/go.work"
unset GOFLAGS
go work init ./certcat ./lib ./bin
go work edit -go="$(awk '$1 == "go" { print $2; exit }' bin/go.mod)"
go -C certcat run ./cmd/certcat-generate -manual data/manual.json -machine data/machine.json -out catalog_gen.go
(cd panel && npm ci && npm run build)
if [[ $mode == manual || $mode == test || $mode == ordinary || $mode == privileged ]]; then
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -o ../dev/winvm/almagestd.exe ./almagestd
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -o ../dev/winvm/almagest-cli.exe ./almagest-cli
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -o ../dev/winvm/windows-test-evidence.exe ./cmd/windows-test-evidence
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -o ../dev/winvm/sandbox-e2e-helper.exe ./cmd/sandbox-e2e-helper
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -o ../dev/winvm/tor-e2e-helper.exe ./cmd/tor-e2e-helper
fi
if [[ $mode == test || $mode == ordinary || $mode == privileged ]]; then
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin test -c \
        -tags=winintegration -covermode=atomic \
        -o ../dev/winvm/almagest-sandbox-matrix.test.exe \
        ./integration/daemon_windows_test.go ./integration/sandbox_windows_test.go
fi
{
    go version
    node --version
    npm --version
} >"$run_dir/generator-versions.txt"
find certcat/catalog_gen.go bin/almagestd/static -type f -print0 | sort -z | xargs -0 sha256sum >"$run_dir/generated.sha256"
tar --exclude=./panel/node_modules --exclude=./panel/public --exclude=./panel/vendor -cf "$run_dir/payload.tar" .
