#!/usr/bin/env bash
set -euo pipefail

test -x /qualification/profile.test
test -x /qualification/launcher
test -x /qualification/probe
test -x /qualification/command
test -d /sys/fs/cgroup

