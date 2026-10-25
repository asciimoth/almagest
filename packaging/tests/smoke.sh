#!/usr/bin/env bash
set -euo pipefail

format="${1:?package format is required}"
arch="${2:?package architecture is required}"

find_package() {
	local pattern="$1"
	local packages=()
	mapfile -t packages < <(find /packages -maxdepth 1 -type f -name "$pattern" -print)
	if ((${#packages[@]} != 1)); then
		printf 'expected one %s package, found %d\n' "$format" "${#packages[@]}" >&2
		exit 1
	fi
	printf '%s\n' "${packages[0]}"
}

install_package() {
	local package="$1"
	case "$format" in
		deb) dpkg --install "$package" ;;
		rpm) rpm --upgrade --replacepkgs "$package" ;;
		archlinux) pacman --upgrade --noconfirm "$package" ;;
		*) printf 'unknown package format: %s\n' "$format" >&2; exit 2 ;;
	esac
}

verify_package() {
	local package="$1"
	local keyring=/tmp/almagest-package-keyring.gpg

	[[ -f "$package.sig" ]] || return 0
	gpg --batch --quiet --dearmor --output "$keyring" /packages/ascii-moth.asc
	gpgv --keyring "$keyring" "$package.sig" "$package"
	if [[ "$format" == archlinux ]]; then
		pacman-key --init
		pacman-key --add /packages/ascii-moth.asc
		pacman-key --lsign-key C5C84658CCFD7E8E71DEE933AF3AE54FC3A35C9F
	fi
}

package_lists_path() {
	local path="$1"
	case "$format" in
		deb) dpkg-query --listfiles almagest | grep -Fx -- "$path" >/dev/null ;;
		rpm) rpm --query --list almagest | grep -Fx -- "$path" >/dev/null ;;
		archlinux) pacman --query --list almagest | awk '{print $2}' | grep -Fx -- "$path" >/dev/null ;;
	esac
}

case "$format" in
	deb) package="$(find_package "*_linux_$arch.deb")" ;;
	rpm) package="$(find_package "*_linux_$arch.rpm")" ;;
	archlinux) package="$(find_package "*_linux_$arch.pkg.tar.zst")" ;;
	*) printf 'unknown package format: %s\n' "$format" >&2; exit 2 ;;
esac

verify_package "$package"
install_package "$package"

test -x /usr/bin/almagest-cli
test -x /usr/libexec/almagest/almagestd
isolation_binaries=(
	almagest-network-role
	almagest-config-writer-role
	almagest-command-role
	almagest-isolation-storage
	almagest-linux-role-launcher
	almagest-subprogram-launcher
)
for binary in "${isolation_binaries[@]}"; do
	if [[ "$arch" == amd64 ]]; then
		test -x "/usr/libexec/almagest/$binary"
	elif [[ -e "/usr/libexec/almagest/$binary" ]]; then
		printf 'arm64 package contains amd64-only isolation binary: %s\n' "$binary" >&2
		exit 1
	fi
done
test -f /usr/share/almagest/locales/en/manifest.json
test -f /usr/share/almagest/locales/en/docs/index.json
test -f /usr/share/almagest/examples/001-hello-network/manifest.json
package_lists_path /usr/share/almagest/locales/en/docs/index.json
if package_lists_path /usr/share/doc/almagest/DAEMON_ISOLATION.md; then
	printf 'package contains developer documentation\n' >&2
	exit 1
fi
if package_lists_path /usr/share/doc/almagest/PACKAGING.md || \
	package_lists_path /usr/share/doc/almagest/PACKAGE_CONTENTS.md; then
	printf 'package installs the package inspection guide\n' >&2
	exit 1
fi
package_lists_path /usr/share/licenses/almagest/LICENSE
test -f /usr/lib/systemd/system/almagestd.service
test -f /usr/lib/sysusers.d/almagest.conf
test -f /usr/lib/tmpfiles.d/almagest.conf
getent group almagest >/dev/null

grep -F -- '--isolation=off --discover-system-programs' \
	/usr/lib/systemd/system/almagestd.service >/dev/null
grep -Fx -- 'StandardOutput=journal' \
	/usr/lib/systemd/system/almagestd.service >/dev/null
grep -Fx -- 'StandardError=journal' \
	/usr/lib/systemd/system/almagestd.service >/dev/null
grep -Fx -- 'SyslogIdentifier=almagestd' \
	/usr/lib/systemd/system/almagestd.service >/dev/null
grep -F -- '"Mode": "auto"' /etc/almagest/protected_alm.hujson >/dev/null
grep -F -- '"DiscoverSystemPrograms": true' \
	/etc/almagest/protected_alm.hujson >/dev/null
grep -F -- '"UnixGroup": "almagest"' \
	/etc/almagest/protected_alm.hujson >/dev/null

# Package updates must keep administrator changes to both config files.
printf '\n// package-smoke-regular\n' >>/etc/almagest/alm.hujson
printf '\n// package-smoke-protected\n' >>/etc/almagest/protected_alm.hujson
install_package "$package"
grep -F -- 'package-smoke-regular' /etc/almagest/alm.hujson >/dev/null
grep -F -- 'package-smoke-protected' /etc/almagest/protected_alm.hujson >/dev/null

/packaging-integration.sh "$format" "$arch"

printf 'package smoke test passed: %s/%s\n' "$format" "$arch"
