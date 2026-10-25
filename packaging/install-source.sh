#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
minimum_go=1.26.9
libseccomp_version=2.6.1
libseccomp_sha256=501f66c667225d53791b97e1d7cf85ab764c297d04881f60f38f451c4b0ee1be

usage() {
	cat <<'EOF'
Usage: packaging/install-source.sh [--check]

Build and install Almagest from this source tree. Use --check to check the
host without building or installing anything.
EOF
}

version_at_least() {
	local actual="$1" minimum="$2"
	local actual_major actual_minor actual_patch minimum_major minimum_minor minimum_patch
	IFS=. read -r actual_major actual_minor actual_patch <<<"$actual"
	IFS=. read -r minimum_major minimum_minor minimum_patch <<<"$minimum"
	actual_patch="${actual_patch%%[^0-9]*}"
	minimum_patch="${minimum_patch%%[^0-9]*}"
	actual_patch="${actual_patch:-0}"
	minimum_patch="${minimum_patch:-0}"
	((10#$actual_major > 10#$minimum_major)) ||
		((10#$actual_major == 10#$minimum_major && 10#$actual_minor > 10#$minimum_minor)) ||
		((10#$actual_major == 10#$minimum_major && 10#$actual_minor == 10#$minimum_minor && 10#$actual_patch >= 10#$minimum_patch))
}

node_version_supported() {
	local version="$1" major minor
	IFS=. read -r major minor _ <<<"$version"
	case "$major" in
		20) ((10#$minor >= 19)) ;;
		22) ((10#$minor >= 12)) ;;
		2[4-9]|[3-9][0-9]) return 0 ;;
		*) return 1 ;;
	esac
}

print_architecture_warning() {
	local architecture="$1"
	[[ "$architecture" == x86_64 ]] && return
	printf '%s\n' \
		'WARNING: Native daemon isolation and subprogram sandboxing have limited support on non-amd64 architectures.' \
		'The installer will use the direct-mode payload without the amd64 isolation roles and native launchers.' >&2
}

host_family() {
	local id='' like=''
	if [[ -r /etc/os-release ]]; then
		# The distribution supplies this file as shell-compatible data.
		# shellcheck disable=SC1091
		. /etc/os-release
		id="${ID:-}"
		like="${ID_LIKE:-}"
	fi
	case " $id $like " in
		*" arch "*) printf '%s\n' arch ;;
		*" debian "*|*" ubuntu "*) printf '%s\n' deb ;;
		*" suse "*|*" opensuse "*) printf '%s\n' suse ;;
		*" fedora "*|*" rhel "*|*" centos "*) printf '%s\n' rpm ;;
		*) printf '%s\n' unknown ;;
	esac
}

append_unique() {
	local value="$1" present
	shift
	for present in "${suggested_packages[@]:-}"; do
		[[ "$present" == "$value" ]] && return
	done
	suggested_packages+=("$value")
}

add_suggestion_packages() {
	local family="$1" requirement="$2"
	case "$family:$requirement" in
		deb:go) append_unique golang-go ;;
		deb:node) append_unique nodejs; append_unique npm ;;
		deb:gcc) append_unique build-essential ;;
		deb:pkgconfig) append_unique pkg-config ;;
		deb:seccomp) append_unique libseccomp-dev ;;
		deb:systemd) append_unique systemd ;;
		deb:sudo) append_unique sudo ;;
		deb:core) append_unique coreutils ;;
		arch:go) append_unique go ;;
		arch:node) append_unique nodejs; append_unique npm ;;
		arch:gcc) append_unique gcc ;;
		arch:pkgconfig) append_unique pkgconf ;;
		arch:seccomp) append_unique libseccomp ;;
		arch:systemd) append_unique systemd ;;
		arch:sudo) append_unique sudo ;;
		arch:core) append_unique coreutils ;;
		suse:go) append_unique go ;;
		suse:node) append_unique nodejs; append_unique npm ;;
		suse:gcc) append_unique gcc ;;
		suse:pkgconfig) append_unique pkg-config ;;
		suse:seccomp) append_unique libseccomp-devel ;;
		suse:systemd) append_unique systemd ;;
		suse:sudo) append_unique sudo ;;
		suse:core) append_unique coreutils ;;
		rpm:go) append_unique golang ;;
		rpm:node) append_unique nodejs; append_unique npm ;;
		rpm:gcc) append_unique gcc ;;
		rpm:pkgconfig) append_unique pkgconf-pkg-config ;;
		rpm:seccomp) append_unique libseccomp-devel ;;
		rpm:systemd) append_unique systemd ;;
		rpm:sudo) append_unique sudo ;;
		rpm:core) append_unique coreutils ;;
		deb:static) append_unique libc6-dev ;;
		arch:static) append_unique glibc ;;
		rpm:static) append_unique glibc-static ;;
		suse:static) append_unique glibc-devel-static ;;
		deb:bootstrap) append_unique curl; append_unique make; append_unique tar; append_unique coreutils; append_unique gperf ;;
		arch:bootstrap) append_unique curl; append_unique make; append_unique tar; append_unique coreutils; append_unique gperf ;;
		rpm:bootstrap) append_unique curl; append_unique make; append_unique tar; append_unique coreutils; append_unique gperf ;;
		suse:bootstrap) append_unique curl; append_unique make; append_unique tar; append_unique coreutils; append_unique gperf ;;
	esac
}

print_install_suggestion() {
	local family="$1"
	((${#suggested_packages[@]})) || return
	printf '\nInstall the missing system packages with:\n' >&2
	case "$family" in
		deb) printf '  sudo apt-get update && sudo apt-get install -y %q' "${suggested_packages[0]}" >&2
			printf ' %q' "${suggested_packages[@]:1}" >&2; printf '\n' >&2 ;;
		arch) printf '  sudo pacman -S --needed %q' "${suggested_packages[0]}" >&2
			printf ' %q' "${suggested_packages[@]:1}" >&2; printf '\n' >&2 ;;
		suse) printf '  sudo zypper install %q' "${suggested_packages[0]}" >&2
			printf ' %q' "${suggested_packages[@]:1}" >&2; printf '\n' >&2 ;;
		rpm) printf '  sudo dnf install -y %q' "${suggested_packages[0]}" >&2
			printf ' %q' "${suggested_packages[@]:1}" >&2; printf '\n' >&2 ;;
	esac
}

check_host() {
	local family architecture go_version='' node_version='' command_name link_probe
	local -a missing=() probe_flags=() probe_ldflags=() bootstrap_commands=()
	declare -g -a suggested_packages=()
	declare -g needs_bundled_seccomp=0
	family="$(host_family)"
	architecture="$(uname -m 2>/dev/null || true)"

	if [[ "$(uname -s 2>/dev/null || true)" != Linux ]]; then
		missing+=("Linux (the source installer installs a systemd service)")
	fi
	case "$architecture" in
		x86_64|aarch64|arm64) ;;
		*) missing+=("a supported CPU architecture (x86_64 or arm64)") ;;
	esac
	if [[ -n "$architecture" ]]; then
		print_architecture_warning "$architecture"
	fi

	for command_name in bash install cp mkdir mktemp rm chmod chown uname; do
		if ! command -v "$command_name" >/dev/null 2>&1; then
			missing+=("$command_name")
			add_suggestion_packages "$family" core
		fi
	done

	if ! command -v go >/dev/null 2>&1; then
		missing+=("Go $minimum_go or newer")
		add_suggestion_packages "$family" go
	else
		go_version="$(go env GOVERSION 2>/dev/null || true)"
		go_version="${go_version#go}"
		if [[ ! "$go_version" =~ ^[0-9]+\.[0-9]+([.][0-9]+)? ]] || ! version_at_least "$go_version" "$minimum_go"; then
			missing+=("Go $minimum_go or newer (found ${go_version:-unknown})")
			add_suggestion_packages "$family" go
		fi
	fi

	if ! command -v node >/dev/null 2>&1 || ! command -v npm >/dev/null 2>&1; then
		missing+=("Node.js 20.19+, 22.12+, or 24+ and npm")
		add_suggestion_packages "$family" node
	else
		node_version="$(node --version 2>/dev/null || true)"
		node_version="${node_version#v}"
		if [[ ! "$node_version" =~ ^[0-9]+\.[0-9]+([.][0-9]+)? ]] || ! node_version_supported "$node_version"; then
			missing+=("Node.js 20.19+, 22.12+, or 24+ (found ${node_version:-unknown})")
			add_suggestion_packages "$family" node
		fi
	fi

	if [[ "$architecture" == x86_64 ]]; then
		if ! command -v gcc >/dev/null 2>&1; then
			missing+=("gcc")
			add_suggestion_packages "$family" gcc
		fi
		if ! command -v pkg-config >/dev/null 2>&1; then
			missing+=("pkg-config")
			add_suggestion_packages "$family" pkgconfig
			missing+=("libseccomp development files")
			add_suggestion_packages "$family" seccomp
		elif ! pkg-config --exists libseccomp; then
			missing+=("libseccomp development files")
			add_suggestion_packages "$family" seccomp
		elif command -v gcc >/dev/null 2>&1 && command -v mktemp >/dev/null 2>&1; then
			link_probe="$(mktemp "${TMPDIR:-/tmp}/almagest-install-link.XXXXXX")"
			read -r -a probe_flags <<<"$(pkg-config --cflags --libs libseccomp)"
			if [[ -n "${ALMAGEST_GLIBC_STATIC_LDFLAG:-}" ]]; then
				read -r -a probe_ldflags <<<"$ALMAGEST_GLIBC_STATIC_LDFLAG"
			fi
			if ! printf '%s\n' '#include <seccomp.h>' 'int main(void) { return seccomp_init(SCMP_ACT_ALLOW) == 0; }' |
				gcc "${probe_ldflags[@]}" -static-pie -x c - "${probe_flags[@]}" -o "$link_probe" >/dev/null 2>&1; then
				if ! printf '%s\n' 'int main(void) { return 0; }' |
					gcc "${probe_ldflags[@]}" -static-pie -x c - -o "$link_probe" >/dev/null 2>&1; then
					missing+=("a static C runtime for gcc")
					add_suggestion_packages "$family" static
				else
					needs_bundled_seccomp=1
					for command_name in curl make tar sha256sum gperf; do
						if ! command -v "$command_name" >/dev/null 2>&1; then
							bootstrap_commands+=("$command_name")
						fi
					done
					if ((${#bootstrap_commands[@]})); then
						missing+=("tools to build the pinned static libseccomp fallback: ${bootstrap_commands[*]}")
						add_suggestion_packages "$family" bootstrap
					fi
				fi
			fi
			rm -f -- "$link_probe"
		fi
	fi

	for command_name in systemctl systemd-sysusers systemd-tmpfiles; do
		if ! command -v "$command_name" >/dev/null 2>&1; then
			missing+=("$command_name")
			add_suggestion_packages "$family" systemd
		fi
	done
	if command -v systemctl >/dev/null 2>&1 && [[ ! -d /run/systemd/system ]]; then
		missing+=("a running systemd system manager")
	fi
	if ((EUID != 0)) && ! command -v sudo >/dev/null 2>&1; then
		missing+=("sudo (or run from a root-owned checkout as root)")
		add_suggestion_packages "$family" sudo
	fi

	if ((${#missing[@]})); then
		printf 'Cannot build and install Almagest. Missing requirements:\n' >&2
		printf '  - %s\n' "${missing[@]}" >&2
		if [[ "$family" == unknown ]]; then
			printf '\nInstall the listed tools with your system package manager.\n' >&2
		else
			print_install_suggestion "$family"
		fi
		if [[ " ${missing[*]} " == *" Go "* || " ${missing[*]} " == *" Node.js "* ]]; then
			printf 'The distribution package must meet the version shown above; use the language vendor package if it does not.\n' >&2
		fi
		return 1
	fi

	printf 'All source-install requirements are available (%s).\n' "$architecture"
}

stage_payload() {
	local stage="$1" architecture="$2"
	local -a common_flags extra_ldflags seccomp_flags
	stage="$(cd -- "$stage" && pwd -P)"

	printf 'Building the admin panel...\n'
	(
		cd "$repo_root/panel"
		npm ci
		npm run build
	)

	install -d "$stage/usr/bin" "$stage/usr/libexec/almagest"
	install -d "$stage/etc/almagest" "$stage/usr/lib/systemd/system"
	install -d "$stage/usr/lib/sysusers.d" "$stage/usr/lib/tmpfiles.d"
	install -d "$stage/usr/share/almagest/locales" "$stage/usr/share/almagest/examples"
	install -d "$stage/usr/share/doc/almagest" "$stage/usr/share/licenses/almagest"

	printf 'Building almagestd and almagest-cli...\n'
	CGO_ENABLED=0 go build -C "$repo_root/bin" -trimpath -o "$stage/usr/libexec/almagest/almagestd" ./almagestd
	CGO_ENABLED=0 go build -C "$repo_root/bin" -trimpath -o "$stage/usr/bin/almagest-cli" ./almagest-cli

	if [[ "$architecture" == x86_64 ]]; then
		printf 'Building the amd64 isolation runtime...\n'
		CGO_ENABLED=0 go build -C "$repo_root/bin" -trimpath -o "$stage/usr/libexec/almagest/almagest-network-role" ./cmd/almagest-network-role
		CGO_ENABLED=0 go build -C "$repo_root/bin" -trimpath -o "$stage/usr/libexec/almagest/almagest-config-writer-role" ./cmd/almagest-config-writer-role
		CGO_ENABLED=0 go build -C "$repo_root/bin" -trimpath -o "$stage/usr/libexec/almagest/almagest-command-role" ./cmd/almagest-command-role
		CGO_ENABLED=0 go build -C "$repo_root/bin" -trimpath -o "$stage/usr/libexec/almagest/almagest-isolation-storage" ./cmd/almagest-isolation-storage
		common_flags=(-static-pie -O2 -D_FORTIFY_SOURCE=3 -fstack-protector-strong -fstack-clash-protection -fno-strict-overflow -Wformat=2 -Wall -Wextra -Werror '-Wl,-z,relro,-z,now,-z,noexecstack')
		extra_ldflags=()
		if [[ -n "${ALMAGEST_GLIBC_STATIC_LDFLAG:-}" ]]; then
			read -r -a extra_ldflags <<<"$ALMAGEST_GLIBC_STATIC_LDFLAG"
		fi
		if ((needs_bundled_seccomp)); then
			build_bundled_seccomp "$stage"
			seccomp_flags=(
				"-I$stage/libseccomp/usr/include"
				"-L$stage/libseccomp/usr/lib"
				-lseccomp
			)
		else
			read -r -a seccomp_flags <<<"$(pkg-config --cflags --libs libseccomp)"
		fi
		gcc "${extra_ldflags[@]}" "${common_flags[@]}" -o "$stage/usr/libexec/almagest/almagest-linux-role-launcher" "$repo_root/bin/native/linux-role/launcher.c" "${seccomp_flags[@]}"
		gcc "${extra_ldflags[@]}" "${common_flags[@]}" -o "$stage/usr/libexec/almagest/almagest-subprogram-launcher" "$repo_root/bin/native/subprogram/launcher.c" "${seccomp_flags[@]}"
	fi

	install -m 0600 "$repo_root/packaging/linux/alm.hujson" "$stage/etc/almagest/alm.hujson"
	install -m 0600 "$repo_root/packaging/linux/protected_alm.hujson" "$stage/etc/almagest/protected_alm.hujson"
	install -m 0644 "$repo_root/packaging/linux/almagestd.service" "$stage/usr/lib/systemd/system/almagestd.service"
	install -m 0644 "$repo_root/packaging/linux/almagest.sysusers" "$stage/usr/lib/sysusers.d/almagest.conf"
	install -m 0644 "$repo_root/packaging/linux/almagest.tmpfiles" "$stage/usr/lib/tmpfiles.d/almagest.conf"
	cp -R "$repo_root/locales/." "$stage/usr/share/almagest/locales/"
	cp -R "$repo_root/examples/." "$stage/usr/share/almagest/examples/"
	install -m 0644 "$repo_root/README.md" "$stage/usr/share/doc/almagest/README.md"
	install -m 0644 "$repo_root/LICENSE" "$stage/usr/share/licenses/almagest/LICENSE"
}

build_bundled_seccomp() {
	local stage="$1"
	local source_root="$stage/libseccomp-source"
	local archive="$stage/libseccomp-$libseccomp_version.tar.gz"
	local url="https://github.com/seccomp/libseccomp/releases/download/v$libseccomp_version/libseccomp-$libseccomp_version.tar.gz"

	printf 'The system has no static libseccomp; building pinned libseccomp %s...\n' "$libseccomp_version"
	curl --fail --location --retry 3 --output "$archive" "$url"
	printf '%s  %s\n' "$libseccomp_sha256" "$archive" | sha256sum --check --status
	install -d "$source_root"
	tar -xzf "$archive" --strip-components=1 -C "$source_root"
	(
		cd "$source_root"
		./configure --prefix=/usr --disable-shared --enable-static
		make
		make DESTDIR="$stage/libseccomp" install
	)
}

install_payload() {
	local stage="${1:?staged payload is required}" was_active=0 path relative
	[[ "$EUID" == 0 ]] || { printf 'The final install phase must run as root.\n' >&2; return 1; }
	[[ -x "$stage/usr/libexec/almagest/almagestd" && -x "$stage/usr/bin/almagest-cli" ]] || {
		printf 'The staged payload is incomplete: %s\n' "$stage" >&2
		return 1
	}

	if systemctl is-active --quiet almagestd.service; then
		was_active=1
		printf 'Stopping the running almagestd service...\n'
		systemctl stop almagestd.service
	fi

	install -d -m 0755 /usr/bin /usr/libexec/almagest /usr/lib/systemd/system
	install -d -m 0755 /usr/lib/sysusers.d /usr/lib/tmpfiles.d
	install -d -m 0755 /etc/almagest /usr/share/almagest/locales
	install -d -m 0755 /usr/share/almagest/examples /usr/share/doc/almagest
	install -d -m 0755 /usr/share/licenses/almagest

	install -m 0755 "$stage/usr/bin/almagest-cli" /usr/bin/almagest-cli
	for path in "$stage"/usr/libexec/almagest/*; do
		install -m 0755 "$path" "/usr/libexec/almagest/${path##*/}"
	done
	for relative in \
		usr/lib/systemd/system/almagestd.service \
		usr/lib/sysusers.d/almagest.conf \
		usr/lib/tmpfiles.d/almagest.conf; do
		install -m 0644 "$stage/$relative" "/$relative"
	done
	for relative in etc/almagest/alm.hujson etc/almagest/protected_alm.hujson; do
		if [[ ! -e "/$relative" ]]; then
			install -m 0600 "$stage/$relative" "/$relative"
		fi
		chown root:root "/$relative"
		chmod 0600 "/$relative"
	done
	cp -R "$stage/usr/share/almagest/locales/." /usr/share/almagest/locales/
	cp -R "$stage/usr/share/almagest/examples/." /usr/share/almagest/examples/
	install -m 0644 "$stage/usr/share/doc/almagest/README.md" /usr/share/doc/almagest/README.md
	install -m 0644 "$stage/usr/share/licenses/almagest/LICENSE" /usr/share/licenses/almagest/LICENSE
	chown -R root:root /usr/share/almagest /usr/share/doc/almagest /usr/share/licenses/almagest

	systemd-sysusers /usr/lib/sysusers.d/almagest.conf
	systemd-tmpfiles --create /usr/lib/tmpfiles.d/almagest.conf
	systemctl daemon-reload
	if ((was_active)); then
		printf 'Restarting almagestd...\n'
		systemctl start almagestd.service
	fi
}

main() {
	local mode="${1:-}" architecture stage
	case "$mode" in
		--help|-h) usage; return ;;
		--check|'') ;;
		--install-stage)
			shift
			install_payload "${1:-}"
			return
			;;
		*) usage >&2; return 2 ;;
	esac

	check_host
	[[ "$mode" == --check ]] && return
	architecture="$(uname -m)"
	[[ "$architecture" == arm64 ]] && architecture=aarch64
	install -d "$repo_root/build"
	stage="$(mktemp -d "$repo_root/build/source-install.XXXXXX")"
	trap 'rm -rf -- "$stage"' EXIT
	stage_payload "$stage" "$architecture"

	printf 'Installing the staged payload into the system...\n'
	if ((EUID == 0)); then
		install_payload "$stage"
	else
		sudo -- "$repo_root/packaging/install-source.sh" --install-stage "$stage"
	fi
	printf '\nAlmagest is installed. Enable and start it with:\n'
	printf '  sudo systemctl enable --now almagestd.service\n'
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
	main "$@"
fi
