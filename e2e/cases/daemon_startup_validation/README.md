# Daemon startup validation

This case starts the instrumented daemon with invalid command-line, config,
storage, secret, TLS, command-runner, system, snapshot, and graph inputs. Each
startup must stop with a specific error. A final valid startup proves that the
failed attempts did not leave shared state behind.
