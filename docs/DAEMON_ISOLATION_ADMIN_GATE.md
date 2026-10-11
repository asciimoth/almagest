# Privileged admin listener gate

Checkpoint 22 is implemented in `bin/internal/isolation/admingate`. The Linux
production composition uses it and publishes listeners only after recovery,
role restriction proofs, and application readiness. Direct mode
continues to create local listeners and passes them directly to its existing
HTTP and admin stream handlers.

## Ownership and protocol boundary

Trusted bootstrap gives N a fixed list of at most eight endpoint IDs, listener
kinds, addresses, and peer filters from protected configuration. N opens the
public listeners. R receives only a `net.Listener` for each fixed endpoint ID.
R cannot add an endpoint, select a path or address, change a filter, or request
process inspection.

The N/R admin lane is separate from control, data, and diagnostic lanes. Both
ends first exchange a fixed 68-byte hello that binds protocol version, session
ID, session epoch, and policy digest. The lane is an inherited authenticated
stream supplied by bootstrap; this package has no named private endpoint or
connect-by-name function. A session mismatch closes the lane.

N opens one multiplexed stream for each accepted public connection and sends
only an eight-byte endpoint header. All later bytes are opaque. N does not
parse HTTP, WebSocket, Origin, panel, pprof, or admin protocol data. The relay
listener in R remains compatible with `http.Server` and the existing stream
codec, so these application checks stay in R.

The multiplexer uses 32 KiB frames, a 256 KiB receive window per stream, and an
8 MiB receive budget for the complete session. N accepts at most 128 concurrent
public connections. R can leave at most 16 connections unclaimed for one
endpoint. A full limit closes the new connection instead of waiting for memory
or a privileged control worker. Each direction has its own copy worker. A slow
client or runtime handler can apply backpressure only to its admin stream. It
does not share a queue, lock, stream, or goroutine with privileged control.
Peer loss and malformed multiplexing close the affected session and wake
blocked listeners and streams.

R-created streams are always closed by N. Thus, the admin lane is not a request
surface into the broker. N supplies no application reply, listener-control,
process-token lookup, or native-handle operation on this lane.

## Native public gates

Web endpoints require a numeric loopback address. On Linux and other supported
Unix systems, TCP peer-owner filters use the existing native protected-listener
rules. Linux Unix sockets use kernel-reported peer ownership. UID, GID, and
startup-resolved username rules are checked in N. A username must still resolve
to its startup UID when a connection arrives. Unknown ownership fails closed.
The Unix socket path must be absolute. An existing non-socket is never removed.

The Windows public named pipe keeps the existing local namespace, owner, DACL,
LocalSystem service, and built-in-administrator behavior. A random name is not
used as authority. The pipe rejects remote clients in the underlying named-pipe
implementation. After accept, N performs its identity probe while locked to one
OS thread. Existing clients request `SecurityAnonymous`, so Windows can withhold
the token user; the protected DACL remains the access decision in that case.
Other token-query failures fail closed. `RevertToSelf` runs before the thread is
unlocked on every successful impersonation path. A failed revert retires the
still-locked OS thread instead of returning it to the runtime pool. No relay,
log, listener, or host mutation work runs while the thread has the client
identity.

Windows TCP username filters inspect only the accepted flow. They require one
unambiguous owner PID, query that process token, compare its SID with the
startup-resolved username SID, and re-resolve the username before allowing the
connection. UID and GID filters are rejected on Windows. The broker does not
expose this lookup as an RPC.

## Verification

Portable tests cover opaque binary relay in both directions, endpoint
separation, runtime HTTP parsing, slow-stream independence, bounded unclaimed
connections, peer failure, shutdown, malformed headers, wrong-direction
streams, identity mismatch, invalid endpoint sets, listener rollback, and
authority-expanding listener inputs. Linux tests use real Unix sockets to check
peer credentials, mode, cleanup, denied peers, relative paths, and non-socket
path safety.

The fuzz targets check the complete hello and stream-header parsers and listener
configuration validation with arbitrary bytes and strings. Use a
network-disabled disposable Docker container for the Linux checks:

```sh
go test -race -cover -timeout 90s ./bin/internal/isolation/admingate
go test ./bin/internal/isolation/admingate -run '^$' -fuzz '^FuzzAdminGateHeaders$' -fuzztime=30s -parallel=2
go test ./bin/internal/isolation/admingate -run '^$' -fuzz '^FuzzListenerConfig$' -fuzztime=30s -parallel=2
```

The Windows Server 2022 amd64 ordinary-user selector is:

```sh
just test-total winvm:m22::privileged-admin-listener-gate
```

The Windows case includes the portable relay suite, actual public named-pipe
creation and collision checks, DACL owner verification by the client, a relayed
pipe stream, fail-closed TCP owner checks, invalid paths, and guaranteed
impersonation revert tests. It does not change routes, firewall, DNS, or TUN
state and does not require elevation.

Bootstrap-created lane delivery and complete-daemon integration are active in
the Linux production profile. Windows service-profile integration remains
unreleased. Current complete-daemon performance is accepted.

### Checkpoint 22 results, 2026-10-10

- The race-enabled package suite passed in a network-disabled Docker container
  on Go 1.26.6 with 76.1% statement coverage. `go vet` also passed there.
- Both 30-second fuzz campaigns passed with two workers. The header campaign
  completed 993,103 executions. The listener configuration campaign completed
  991,700 executions.
- Windows amd64 and arm64 cross-builds and the focused Linux lint passed. The
  Windows Server 2022 amd64 ordinary-user case passed in
  `artifacts/winvm/run-1clvLHj1`. It validated every registered test and fuzz
  seed without elevation.
- The first Windows native run found that the token-user probe rejected the
  existing CLI client's `SecurityAnonymous` pipe connection after its DACL had
  admitted it. The final implementation preserves that established DACL access
  contract only for the two exact anonymous/impersonation-level query errors.
  Other query failures still reject the connection. The final VM run verified
  the corrected behavior through a real pipe.
- The direct daemon does not import the isolation admin gate and creates no
  admin relay. The later Linux production N composition uses the gate. No
  route, firewall, DNS, or TUN state changed in these checkpoint-22 checks.
- Focused `just check` passed. It included formatting, spelling, secret
  scanning, panel generation, Linux and Windows amd64/arm64 lint, vet, harness
  tests, and the selected race-enabled relay test.
