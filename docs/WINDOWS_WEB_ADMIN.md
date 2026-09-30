# Windows web administration

Windows web administration is disabled by default. Set `Admin.Web` or use
`--web` to enable the loopback listener. To limit access by local account,
configure `Admin.WebFilters.Usernames`, `--web-filter-user`, or
`ALMAGEST_WEB_ADMIN_FILTER_USERS`.

Example protected config:

```hujson
Admin: {
  Web: 8080
  WebFilters: {
    Usernames: ["DOMAIN\\alice"]
  }
}
```

The filter applies to every accepted connection before HTTP processing. Thus,
it covers the panel, `/api/ws`, and `/debug/pprof/` when pprof is enabled. The
daemon does these checks:

1. It resolves each configured account name to a Windows SID when it creates
   the listener.
2. It finds the process that owns each accepted TCP client socket.
3. It reads that process token's user SID.
4. It resolves the configured account name again. The current SID, original
   SID, and process-token SID must all be equal.

The daemon denies a connection if socket ownership is missing or ambiguous, if
the client exits during inspection, if its token is inaccessible, or if an
account name now resolves to a different SID. Windows does not support the
`UIDs` or `GIDs` web filter fields. Their config fields and command-line flags
cause startup to fail instead of being ignored.

The username filter is optional, as it is on Linux. If no web peer-owner filter
is configured, the daemon starts the listener and warns that any local user can
access it. Loopback binding does not isolate Windows users from one another.

## Security limits

This is a best-effort local-user limit. It is not authentication and it is not
a strong security boundary. Socket-table lookup and process inspection are
race-prone. A process that runs as an allowed user is trusted, including an
untrusted web page that can make requests through that user's browser when
cross-origin access is enabled. Processes with sufficient rights to inspect,
inject into, or control an allowed process are outside this filter's boundary.

Keep `AllowCrossOrigin` disabled unless it is necessary. The same-origin check
reduces browser request forgery, but it does not authenticate a user. Prefer
the Windows named-pipe API for local CLI administration. A future API-token
system will provide a strong web authentication feature; it is outside the
Windows backend integration scope.
