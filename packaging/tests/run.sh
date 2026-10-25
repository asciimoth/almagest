#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
cd "$repo_root"

archive_count="$(find dist -maxdepth 1 -type f -name 'almagest_*_linux_amd64.tar.gz' | wc -l)"
if [[ "$archive_count" != "1" ]]; then
	printf 'expected one Linux amd64 archive, found %s\n' "$archive_count" >&2
	exit 1
fi

archive="$(find dist -maxdepth 1 -type f -name 'almagest_*_linux_amd64.tar.gz' -print)"
test -f dist/PACKAGE_CONTENTS.md
test -f dist/ascii-moth.asc

if [[ -f "$archive.sig" ]]; then
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

	verify_artifact "$archive"
	verify_artifact dist/checksums.txt
	for package in dist/*.deb dist/*.rpm dist/*.pkg.tar.zst; do
		verify_artifact "$package"
	done
elif find dist -maxdepth 1 -type f -name '*.sig' -print -quit | grep -q .; then
	printf 'release artifact signatures are incomplete\n' >&2
	exit 1
fi

tar -tzf "$archive" | grep -E '(^|/)libexec/almagest/almagestd$' >/dev/null
tar -tzf "$archive" | grep -E '(^|/)bin/almagest-cli$' >/dev/null
tar -tzf "$archive" | grep -E '(^|/)share/almagest/locales/en/manifest.json$' >/dev/null
tar -tzf "$archive" | grep -E '(^|/)share/almagest/locales/en/docs/index.json$' >/dev/null
tar -tzf "$archive" | grep -E '(^|/)share/almagest/examples/001-hello-network/manifest.json$' >/dev/null
tar -tzf "$archive" | grep -E '(^|/)PACKAGE_CONTENTS\.md$' >/dev/null
if tar -tzf "$archive" | grep -E '(^|/)share/doc/almagest/DAEMON_ISOLATION\.md$' >/dev/null; then
	printf 'archive contains developer documentation\n' >&2
	exit 1
fi

(
	cd dist
	sha256sum --check checksums.txt
)

formats=(deb rpm archlinux)
bases=(
	debian:bookworm-slim
	rockylinux:9
	archlinux:base
)

for index in "${!formats[@]}"; do
	format="${formats[$index]}"
	image="almagest-package-test-$format:local"
	printf '==> packaging: testing %s\n' "$format"
	docker build --platform linux/amd64 \
		--build-arg "BASE_IMAGE=${bases[$index]}" \
		-f packaging/tests/Dockerfile \
		-t "$image" .
	docker run --rm --platform linux/amd64 \
		--privileged --device /dev/net/tun \
		"$image" "$format"
done
