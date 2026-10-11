# Linux isolated deployment

Install these root-owned, non-writable files in one directory, normally
`/usr/libexec/almagest`:

- `almagestd`
- `almagest-network-role`
- `almagest-linux-role-launcher`
- `almagest-config-writer-role`
- `almagest-command-role`
- `almagest-isolation-storage`

Install `almagest.sysusers`, `almagest.tmpfiles`, and `almagestd.service` in the
distribution's systemd directories. Run `systemd-sysusers` and
`systemd-tmpfiles --create` before the first start. Do not make the native
launcher setuid and do not add file capabilities to any role binary.

`ExecStartPre` prepares a fresh layout or migrates the stopped legacy
`/var/lib/almagest/store` layout. It keeps the original tree as
`store.pre-isolation` and keeps an identity-bound migration receipt. It also
copies the old regular config to the separate writer-owned directory. The
root-owned protected config remains under `/etc/almagest`.

For interrupted setup, keep the daemon stopped and run:

```text
almagest-isolation-storage recover
```

To return storage to direct mode, first recover all native resources and make
sure the broker journal is empty. Then stop the service and run:

```text
almagest-isolation-storage rollback
almagestd --no-isolation --config /etc/almagest/alm.hujson
```

Rollback refuses to remove an unproved tree or a non-empty broker journal. It
does not silently discard host-effect recovery evidence. Package updates must
replace all six binaries atomically while the service is stopped. A partial
binary set fails trusted-path or protocol validation; it never falls back to
direct mode.
