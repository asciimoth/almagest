# Windows local administration pipe

Almagest uses a local Windows named pipe for the stream administration API.
The pipe reuses the same message codec and API sessions as the Unix socket.

An interactive daemon uses this endpoint:

```text
\\.\pipe\almagest-admin-user-<current-user-SID>
```

Its ACL permits the current identity and Local System. The pipe owner is the
current identity. The default CLI endpoint is `pipe://user`. The CLI calculates
the same SID-scoped name and rejects a pipe that is not owned by its current
identity.

A Windows service uses this endpoint:

```text
\\.\pipe\almagest-admin
```

The supported service identity for this endpoint is Local System. Its ACL
permits Local System and elevated administrators. A restricted UAC token does
not receive access through the Administrators ACE. Use `pipe://machine` from
an elevated CLI. The CLI rejects a machine pipe that is not owned by Local
System.

Both listeners reject remote clients. First-instance creation fails if another
server already owns the name. Thus, a second daemon cannot replace an active
endpoint.

Use `Admin.Pipe`, `--pipe`, or `ALMAGEST_PIPE_ADMIN` to set a complete local
pipe path. An empty value disables it. For isolated tests, select a unique
single-component name in both processes:

```text
almagestd --pipe \\.\pipe\almagest-test-123
almagest-cli --url pipe://user/almagest-test-123 admin ping
```

Use the `machine` trust class for a test daemon that runs as a Local System
service. Names cannot contain a slash or select a remote computer. The user
and machine defaults are different, so an omitted endpoint does not mix
interactive and service state.
