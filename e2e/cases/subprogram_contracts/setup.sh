#!/usr/bin/env bash
set -euo pipefail
test -x /usr/local/bin/almagest-subprogram-launcher
test -x /usr/local/bin/almagest-sandbox-e2e-helper
sed -i "s@%E2E_RUNTIME%@$E2E_RUNTIME@g" "$E2E_CONFIG_DIR/protected_alm.hujson"
install -d -o 0 -g 0 -m 0700 \
    "$E2E_RUNTIME/sandbox-temporary" "$E2E_RUNTIME/sandbox-registry"
for name in sandbox subprogram-policy subprogram-manager subprogram-direct subprogram-service; do
    test -x "/qualification/$name.test"
done
