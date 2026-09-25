#!/usr/bin/env bash
set -euo pipefail

command -v jq >/dev/null
command -v setpriv >/dev/null
command -v tor >/dev/null
test -x /opt/chutney/chutney
id tor-test >/dev/null
