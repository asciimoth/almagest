#!/usr/bin/env bash
set -euo pipefail

test -x /qualification/profile.test
test -x /qualification/launcher
test -x /qualification/probe
test -x /qualification/command
test -x /qualification/subprogram-direct.test
test -x /usr/local/bin/almagest-subprogram-launcher
test -d /sys/fs/cgroup

sandbox_cgroup="$E2E_CGROUP_PARENT/almagest-e2e-sandbox-workers"
mkdir "$sandbox_cgroup"
printf '%s\n' '+cpu +memory +pids' >"$sandbox_cgroup/cgroup.subtree_control"
