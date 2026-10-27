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
	deb) package="$(find_package "almagest_*_linux_$arch.deb")" ;;
	rpm) package="$(find_package "almagest_*_linux_$arch.rpm")" ;;
	archlinux) package="$(find_package "almagest_*_linux_$arch.pkg.tar.zst")" ;;
	*) printf 'unknown package format: %s\n' "$format" >&2; exit 2 ;;
esac

app_package=""
if [[ "$arch" == amd64 ]]; then
	case "$format" in
		deb) app_package="$(find_package "almagest-app_*_linux_amd64.deb")" ;;
		rpm) app_package="$(find_package "almagest-app_*_linux_amd64.rpm")" ;;
		archlinux) app_package="$(find_package "almagest-app_*_linux_amd64.pkg.tar.zst")" ;;
	esac
elif find /packages -maxdepth 1 -type f -name 'almagest-app_*_linux_arm64*' -print -quit | grep -q .; then
	printf 'arm64 artifacts include the unsupported desktop app\n' >&2
	exit 1
fi

verify_package "$package"
install_package "$package"
if [[ -e /usr/bin/almagest-app ]]; then
	printf 'daemon package contains the desktop app\n' >&2
	exit 1
fi

if [[ -n "$app_package" ]]; then
	verify_package "$app_package"
	case "$format" in
		deb) apt-get update && apt-get install --yes "$app_package" ;;
		rpm) dnf install --assumeyes "$app_package" ;;
		archlinux) install_package "$app_package" ;;
	esac
	test -x /usr/bin/almagest-app
	test -f /usr/share/applications/almagest-app.desktop
	test -f /etc/xdg/autostart/almagest-app.desktop
	test -f /usr/share/icons/hicolor/128x128/apps/almagest.png
	app_package_lists_path() {
		local path="$1"
		case "$format" in
			deb) dpkg-query --listfiles almagest-app | grep -Fx -- "$path" >/dev/null ;;
			rpm) rpm --query --list almagest-app | grep -Fx -- "$path" >/dev/null ;;
			archlinux) pacman --query --list almagest-app | awk '{print $2}' | grep -Fx -- "$path" >/dev/null ;;
		esac
	}
	app_package_lists_path /usr/bin/almagest-app
	app_package_lists_path /etc/xdg/autostart/almagest-app.desktop
	app_package_lists_path /usr/share/licenses/almagest-app/LICENSE
	/usr/bin/almagest-app --help | grep -F -- \
		'almagest-app [--background] [--url ENDPOINT]' >/dev/null
fi

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
find /usr/share/almagest/examples -mindepth 2 -maxdepth 2 -type f \
	-name manifest.json -print -quit | grep -q .
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
