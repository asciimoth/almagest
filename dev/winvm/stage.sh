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
if [[ $mode == manual || $mode == package || $mode == package-smoke || $mode == package-lifecycle || $mode == package-integration || $mode == test || $mode == ordinary || $mode == privileged ]]; then
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -trimpath -o ../dev/winvm/almagestd.exe ./almagestd
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -trimpath -o ../dev/winvm/almagest-cli.exe ./almagest-cli
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -trimpath -o ../dev/winvm/llvm-as-wrapper.exe ./cmd/winvm-llvm-as-wrapper
fi
if [[ $mode == manual || $mode == test || $mode == ordinary || $mode == privileged ]]; then
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -o ../dev/winvm/windows-test-evidence.exe ./cmd/windows-test-evidence
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -o ../dev/winvm/sandbox-e2e-helper.exe ./cmd/sandbox-e2e-helper
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build \
        -o ../dev/winvm/tor-e2e-helper.exe ./cmd/tor-e2e-helper
fi
if [[ $mode == package || $mode == package-smoke || $mode == package-lifecycle || $mode == package-integration ]]; then
    package_version=${ALMAGEST_PACKAGE_VERSION:?ALMAGEST_PACKAGE_VERSION is required for package mode}
    dependency_root=dev/winvm/package-inputs
    mkdir -p "$dependency_root/wintun/amd64" "$dependency_root/wintun/licenses" \
        "$dependency_root/split-driver/amd64" "$dependency_root/split-driver/licenses"
    cp "$SYSNET_WINDOWS_WINTUN_DIR/amd64/wintun.dll" "$dependency_root/wintun/amd64/"
    cp "$SYSNET_WINDOWS_WINTUN_DIR/licenses/wintun-LICENSE.txt" "$dependency_root/wintun/licenses/"
    cp "$MULLVAD_SPLIT_TUNNEL_DRIVER_DIR/amd64/"* "$dependency_root/split-driver/amd64/"
    cp "$MULLVAD_SPLIT_TUNNEL_DRIVER_DIR/licenses/mullvad-LICENSE-MPL.txt" \
        "$dependency_root/split-driver/licenses/"
    cp "$run_dir/source.json" dev/winvm/source.json
    python3 packaging/windows/create-build-manifest.py \
        --version "$package_version" --root "$PWD" \
        --source-record dev/winvm/source.json --image-lock dev/winvm/image-lock.json \
        --output dev/winvm/package-manifest.json
    if [[ $mode == package-lifecycle ]]; then
        IFS=. read -r version_major version_minor version_patch <<<"$package_version"
        if ((version_patch < 65535)); then
            upgrade_version="$version_major.$version_minor.$((version_patch + 1))"
        elif ((version_minor < 255)); then
            upgrade_version="$version_major.$((version_minor + 1)).0"
        elif ((version_major < 255)); then
            upgrade_version="$((version_major + 1)).0.0"
        else
            printf 'package lifecycle version has no valid MSI successor: %s\n' "$package_version" >&2
            exit 1
        fi
        python3 packaging/windows/create-build-manifest.py \
            --version "$upgrade_version" --root "$PWD" \
            --source-record dev/winvm/source.json --image-lock dev/winvm/image-lock.json \
            --output dev/winvm/package-upgrade-manifest.json
    fi
    mkdir .cargo
    cargo vendor --locked --versioned-dirs \
        --manifest-path app/src-tauri/Cargo.toml dev/winvm/cargo-vendor \
        >.cargo/config.toml
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
    cargo --version
} >"$run_dir/generator-versions.txt"
generated=(certcat/catalog_gen.go bin/almagestd/static)
[[ $mode != package && $mode != package-smoke && $mode != package-lifecycle && $mode != package-integration ]] || generated+=(dev/winvm/package-manifest.json)
[[ $mode != package-lifecycle ]] || generated+=(dev/winvm/package-upgrade-manifest.json)
find "${generated[@]}" -type f -print0 | sort -z | xargs -0 sha256sum >"$run_dir/generated.sha256"
tar --exclude=./panel/node_modules --exclude=./panel/public --exclude=./panel/vendor -cf "$run_dir/payload.tar" .
