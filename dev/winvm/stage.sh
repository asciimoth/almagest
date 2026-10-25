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
if [[ $mode == manual || $mode == package || $mode == test || $mode == ordinary || $mode == privileged ]]; then
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -trimpath -o ../dev/winvm/almagestd.exe ./almagestd
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -trimpath -o ../dev/winvm/almagest-cli.exe ./almagest-cli
fi
if [[ $mode == manual || $mode == test || $mode == ordinary || $mode == privileged ]]; then
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -o ../dev/winvm/windows-test-evidence.exe ./cmd/windows-test-evidence
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -o ../dev/winvm/sandbox-e2e-helper.exe ./cmd/sandbox-e2e-helper
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -o ../dev/winvm/tor-e2e-helper.exe ./cmd/tor-e2e-helper
fi
if [[ $mode == package ]]; then
    package_version=${ALMAGEST_PACKAGE_VERSION:?ALMAGEST_PACKAGE_VERSION is required for package mode}
    dependency_root=dev/winvm/package-inputs
    mkdir -p "$dependency_root/wintun/amd64" "$dependency_root/wintun/licenses" \
        "$dependency_root/split-driver/amd64" "$dependency_root/split-driver/licenses"
    cp "$SYSNET_WINDOWS_WINTUN_DIR/amd64/wintun.dll" "$dependency_root/wintun/amd64/"
    cp "$SYSNET_WINDOWS_WINTUN_DIR/licenses/wintun-LICENSE.txt" "$dependency_root/wintun/licenses/"
    cp "$MULLVAD_SPLIT_TUNNEL_DRIVER_DIR/amd64/"* "$dependency_root/split-driver/amd64/"
    cp "$MULLVAD_SPLIT_TUNNEL_DRIVER_DIR/licenses/mullvad-LICENSE-MPL.txt" \
        "$dependency_root/split-driver/licenses/"
    python3 packaging/windows/create-build-manifest.py \
        --version "$package_version" --root "$PWD" \
        --source-record "$run_dir/source.json" --image-lock dev/winvm/image-lock.json \
        --output dev/winvm/package-manifest.json
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
generated=(certcat/catalog_gen.go bin/almagestd/static)
[[ $mode != package ]] || generated+=(dev/winvm/package-manifest.json)
find "${generated[@]}" -type f -print0 | sort -z | xargs -0 sha256sum >"$run_dir/generated.sha256"
tar --exclude=./panel/node_modules --exclude=./panel/public --exclude=./panel/vendor -cf "$run_dir/payload.tar" .
