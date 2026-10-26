#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
cd "$repo_root"

architectures=(amd64 arm64)
declare -A archives=()
for arch in "${architectures[@]}"; do
	archive_count="$(find dist -maxdepth 1 -type f -name "almagest_*_linux_$arch.tar.gz" | wc -l)"
	if [[ "$archive_count" != "1" ]]; then
		printf 'expected one Linux %s archive, found %s\n' "$arch" "$archive_count" >&2
		exit 1
	fi
	archives[$arch]="$(find dist -maxdepth 1 -type f -name "almagest_*_linux_$arch.tar.gz" -print)"
done
app_archive_count="$(find dist -maxdepth 1 -type f -name "almagest-app_*_linux_amd64.tar.gz" | wc -l)"
if [[ "$app_archive_count" != "1" ]]; then
	printf 'expected one Linux amd64 app archive, found %s\n' "$app_archive_count" >&2
	exit 1
fi
app_archive="$(find dist -maxdepth 1 -type f -name "almagest-app_*_linux_amd64.tar.gz" -print)"
if find dist -maxdepth 1 -type f -name 'almagest-app_*_linux_arm64*' -print -quit | grep -q .; then
	printf 'found an unsupported Linux arm64 app artifact\n' >&2
	exit 1
fi
test -f dist/PACKAGE_CONTENTS.md
test -f dist/ascii-moth.asc

if [[ -f "${archives[amd64]}.sig" ]]; then
	verify_home="$(mktemp -d)"
	trap 'rm -rf -- "$verify_home"' EXIT
	chmod 0700 "$verify_home"
	gpg --batch --quiet --dearmor --output "$verify_home/ascii-moth.gpg" \
		dist/ascii-moth.asc

	verify_artifact() {
		local artifact="$1"
		test -f "$artifact.sig"
		gpgv --keyring "$verify_home/ascii-moth.gpg" \
			"$artifact.sig" "$artifact"
	}

	for archive in "${archives[@]}"; do
		verify_artifact "$archive"
	done
	verify_artifact "$app_archive"
	verify_artifact dist/checksums.txt
	for package in dist/*.deb dist/*.rpm dist/*.pkg.tar.zst; do
		verify_artifact "$package"
	done
elif find dist -maxdepth 1 -type f -name '*.sig' -print -quit | grep -q .; then
	printf 'release artifact signatures are incomplete\n' >&2
	exit 1
fi

for arch in "${architectures[@]}"; do
	archive="${archives[$arch]}"
	listing="$(tar -tzf "$archive")"
	grep -E '(^|/)libexec/almagest/almagestd$' <<<"$listing" >/dev/null
	grep -E '(^|/)bin/almagest-cli$' <<<"$listing" >/dev/null
	grep -E '(^|/)share/almagest/locales/en/manifest.json$' <<<"$listing" >/dev/null
	grep -E '(^|/)share/almagest/locales/en/docs/index.json$' <<<"$listing" >/dev/null
	grep -E '(^|/)share/almagest/examples/001-hello-network/manifest.json$' <<<"$listing" >/dev/null
	grep -E '(^|/)PACKAGE_CONTENTS\.md$' <<<"$listing" >/dev/null
	if grep -E '(^|/)almagest-app$' <<<"$listing" >/dev/null; then
		printf '%s daemon archive contains the desktop app\n' "$arch" >&2
		exit 1
	fi
	if grep -E '(^|/)share/doc/almagest/DAEMON_ISOLATION\.md$' <<<"$listing" >/dev/null; then
		printf '%s archive contains developer documentation\n' "$arch" >&2
		exit 1
	fi
	for binary in almagest-network-role almagest-config-writer-role \
		almagest-command-role almagest-isolation-storage \
		almagest-linux-role-launcher almagest-subprogram-launcher; do
		if [[ "$arch" == amd64 ]]; then
			grep -E "(^|/)libexec/almagest/$binary$" <<<"$listing" >/dev/null
		elif grep -E "(^|/)libexec/almagest/$binary$" <<<"$listing" >/dev/null; then
			printf 'arm64 archive contains amd64-only isolation binary: %s\n' "$binary" >&2
			exit 1
		fi
	done
done

app_listing="$(tar -tzf "$app_archive")"
grep -E '(^|/)almagest-app$' <<<"$app_listing" >/dev/null
grep -E '(^|/)share/applications/almagest-app.desktop$' <<<"$app_listing" >/dev/null
grep -E '(^|/)etc/xdg/autostart/almagest-app.desktop$' <<<"$app_listing" >/dev/null
grep -E '(^|/)share/icons/hicolor/128x128/apps/almagest.png$' <<<"$app_listing" >/dev/null
grep -E '(^|/)share/licenses/almagest-app/LICENSE$' <<<"$app_listing" >/dev/null
grep -E '(^|/)PACKAGE_CONTENTS\.md$' <<<"$app_listing" >/dev/null
if grep -E '(^|/)(almagest-cli|almagestd)$' <<<"$app_listing" >/dev/null; then
	printf 'desktop app archive contains daemon package binaries\n' >&2
	exit 1
fi

(
	cd dist
	sha256sum --check checksums.txt
)

read -r -a formats <<<"${ALMAGEST_PACKAGE_TEST_FORMATS:-deb rpm archlinux}"

read -r -a test_architectures <<<"${ALMAGEST_PACKAGE_TEST_ARCHES:-amd64 arm64}"
for arch in "${test_architectures[@]}"; do
	if [[ "$arch" != amd64 && "$arch" != arm64 ]]; then
		printf 'unsupported package-test architecture: %s\n' "$arch" >&2
		exit 2
	fi
	for format in "${formats[@]}"; do
		case "$format/$arch" in
			deb/*) base=debian:bookworm-slim ;;
			rpm/amd64) base=fedora:43 ;;
			rpm/arm64) base=rockylinux:9 ;;
			archlinux/amd64) base=archlinux:base ;;
			archlinux/arm64) base=menci/archlinuxarm:base ;;
			*) printf 'unsupported package-test format: %s\n' "$format" >&2; exit 2 ;;
		esac
		image="almagest-package-test-$format-$arch:local"
		printf '==> packaging: testing %s/%s\n' "$format" "$arch"
		docker build --platform "linux/$arch" \
			--build-arg "BASE_IMAGE=$base" \
			-f packaging/tests/Dockerfile \
			-t "$image" .
		docker run --rm --platform "linux/$arch" \
			--privileged --device /dev/net/tun \
			"$image" "$format" "$arch"
	done
done
