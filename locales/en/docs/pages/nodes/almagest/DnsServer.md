Accepts [DNS](https://www.rfc-editor.org/rfc/rfc1034) requests over
[UDP](https://www.rfc-editor.org/rfc/rfc768) through an attached Network and
sends each request to an attached DNS resolver. Connect the listening network to
[Network](@port/right/Network) and the resolver that will answer requests to
[DNS](@port/right/DNS).

This node provides a UDP listener only. It does not provide
[DNS over TCP](https://www.rfc-editor.org/rfc/rfc7766) or
[DNS over TLS](https://www.rfc-editor.org/rfc/rfc7858).

## Listen address

Use the **Network** setting to select the UDP address family:

- **UDP** lets the attached Network select the family from the bind address.
- **UDP4** uses IPv4 only.
- **UDP6** uses IPv6 only.

**Host** and **Port** set the local bind address. The defaults are
`127.0.0.1:5353`. For a local IPv6 listener, use `::1`. The port must be from 1
through 65535.

Use a loopback address when only local clients must reach the server. Binding to
`0.0.0.0` or `::` can expose the resolver on all interfaces available through
the attached Network. Use an all-interface address only when access to the
listener is controlled. An exposed DNS resolver can be used for unwanted queries
or DNS amplification attacks.

## Request timeout

**Request timeout (ms)** limits how long one received request can wait for the
attached DNS resolver. Set the value to `0` to use the default timeout of 5
seconds. Negative values are not valid. If the resolver returns an error or the
request times out, the server replies with `SERVFAIL`.

The listener can stay open without a ready [DNS](@port/right/DNS) input. In this
state, the node shows `waiting for DNS` and replies to valid requests with
`SERVFAIL`. Malformed DNS packets are ignored.

## Listener changes and errors

Changing the UDP family, host, port, timeout, or attached
[Network](@port/right/Network) closes the current listener and starts a new
listener. Requests can fail during this short restart. Changing only the
attached [DNS](@port/right/DNS) resolver keeps the listener open, but cancels
requests that were waiting on the previous resolver.

If the node cannot bind the configured address, it shows **listen failed** and
records the cause in its log. The node retries the bind once per second while
the [Network](@port/right/Network) remains ready. When the listener and
[DNS](@port/right/DNS) input are ready, the node label shows the address on
which it is listening.
