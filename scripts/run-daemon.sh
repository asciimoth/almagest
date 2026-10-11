#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
payload="$repo_root/build/isolation-linux"
elevated=0
direct=0
experimental=0

if [[ ${1:-} == --sudo ]]; then
    elevated=1
    shift
fi

# just passes its variadic recipe parameter as one quoted string. Only these
# two exact, whitespace-free daemon flags are accepted here.
read -r -a requested <<<"${1:-}"
for option in "${requested[@]}"; do
    case "$option" in
        --no-isolation) direct=1 ;;
        --experimental) experimental=1 ;;
        *)
            printf 'unsupported daemon recipe flag: %s\n' "$option" >&2
            printf 'usage: just daemon%s [--no-isolation] [--experimental]\n' "$([[ $elevated == 1 ]] && printf '%s' '-sudo')" >&2
            exit 2
            ;;
    esac
done

if [[ $experimental == 1 && $direct != 1 ]]; then
    printf '%s\n' '--experimental requires explicit --no-isolation' >&2
    exit 2
fi

common=(-l debug -w 8090 --debug-nodes --no-dns-cache-expiration)
if [[ $direct == 1 ]]; then
    arguments=(--no-isolation -c "$repo_root/local" --unsafe-config "${common[@]}")
    if [[ $elevated != 1 ]]; then
        arguments+=(--tmp-unix-admin)
    fi
else
    # The elevated runner supplies a role-owned copy of local config. The
    # ordinary runner uses the provisioned system config and storage.
    arguments=("${common[@]}")
fi
if [[ $experimental == 1 ]]; then
    arguments+=(--experimental)
fi

executable="$payload/almagestd"
command=("$executable" "${arguments[@]}")
if [[ $elevated == 1 ]]; then
    if [[ $direct == 1 ]]; then
        command=(sudo -- "${command[@]}")
    else
        # A root daemon must not trust executables below a user-owned checkout.
        # Stage the complete sibling payload below a root-owned runtime path.
        # Persistent development data has separate role ownership below /var.
        caller_uid=$UID
        caller_gid=$(id -g)
        state="/var/lib/almagest/dev-${caller_uid}"
        stage="/run/almagest-dev-${caller_uid}-PRINT"
        if [[ ${ALMAGEST_DAEMON_PRINT_COMMAND:-0} != 1 ]]; then
            exec 9>"$repo_root/local/.daemon-sudo.lock"
            if ! flock -n 9; then
                printf 'another daemon-sudo instance owns the local development config\n' >&2
                exit 1
            fi
            for account in almagest-runtime almagest-writer almagest-command; do
                if ! id "$account" >/dev/null 2>&1; then
                    printf 'required isolation account %s is absent; install packaging/linux/almagest.sysusers\n' "$account" >&2
                    exit 1
                fi
            done
            runtime_uid=$(id -u almagest-runtime)
            runtime_gid=$(id -g almagest-runtime)
            writer_uid=$(id -u almagest-writer)
            writer_gid=$(id -g almagest-writer)
            command_uid=$(id -u almagest-command)
            command_gid=$(id -g almagest-command)
            stage=$(sudo mktemp -d "/run/almagest-dev-${caller_uid}-XXXXXXXX")
            sudo chmod 0755 -- "$stage"
            sudo install -o root -g root -m 0755 -- \
                "$payload/almagestd" \
                "$payload/almagest-network-role" \
                "$payload/almagest-config-writer-role" \
                "$payload/almagest-command-role" \
                "$payload/almagest-isolation-storage" \
                "$payload/almagest-linux-role-launcher" \
                "$stage/"
            sudo install -d -o root -g root -m 0755 -- /var/lib/almagest "$state"
            sudo install -d -o "$writer_uid" -g "$writer_gid" -m 0700 -- "$state/config"
            sudo install -d -o root -g root -m 0700 -- "$state/protected"
            sudo install -d -o "$runtime_uid" -g "$runtime_gid" -m 0700 -- "$state/runtime"
            sudo install -d -o "$command_uid" -g "$command_gid" -m 0700 -- "$state/helper"
            sudo install -d -o root -g root -m 0700 -- "$state/broker"
            sudo install -o "$writer_uid" -g "$writer_gid" -m 0600 -- \
                "$repo_root/local/alm.hujson" "$state/config/alm.hujson"
            sudo install -o root -g root -m 0600 -- \
                "$repo_root/local/protected_alm.hujson" "$state/protected/protected_alm.hujson"
        fi
        command=(sudo -- systemd-run --scope --quiet --property=Delegate=yes \
            --setenv="ALMAGEST_ISOLATION_DEVELOPMENT_ROOT=$state" -- \
            "$stage/almagestd" "${arguments[@]}" --unix "$stage/admin.sock")
    fi
fi

if [[ ${ALMAGEST_DAEMON_PRINT_COMMAND:-0} == 1 ]]; then
    printf '%q ' "${command[@]}"
    printf '\n'
    exit 0
fi

if [[ $elevated == 1 && $direct != 1 ]]; then
    local_config="$repo_root/local/alm.hujson"
    local_mode=$(stat -c '%a' -- "$local_config")
    copyback=''
    # shellcheck disable=SC2329  # Invoked by the EXIT trap.
    cleanup() {
        status=$?
        trap - EXIT
        if [[ -n $copyback ]]; then
            rm -f -- "$copyback"
        fi
        copyback=$(mktemp "$repo_root/local/.alm.hujson.copyback.XXXXXXXX")
        if sudo install -o "$caller_uid" -g "$caller_gid" -m "$local_mode" -- \
            "$state/config/alm.hujson" "$copyback" && mv -f -- "$copyback" "$local_config"; then
            copyback=''
        else
            rm -f -- "$copyback"
            copyback=''
            printf 'failed to copy isolated config back to %s\n' "$local_config" >&2
            [[ $status != 0 ]] || status=1
        fi
        if [[ $stage == "/run/almagest-dev-${caller_uid}-"???????? ]]; then
            if ! sudo rm -rf --one-file-system -- "$stage"; then
                printf 'failed to remove isolated daemon staging directory %s\n' "$stage" >&2
                [[ $status != 0 ]] || status=1
            fi
        else
            printf 'refusing to remove unexpected staging directory %s\n' "$stage" >&2
            [[ $status != 0 ]] || status=1
        fi
        exit "$status"
    }
    trap cleanup EXIT
    "${command[@]}"
    exit $?
fi

exec "${command[@]}"
