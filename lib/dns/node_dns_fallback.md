# DNS fallback

`DnsFallback` tries DNS upstreams in ascending slot order. It returns the first
`NOERROR` response. It tries the next attached slot when an upstream returns a
Go error, a DNS error response such as `NXDOMAIN` or `SERVFAIL`, or no response
before the attempt timeout.

The `Attempt timeout (ms)` menu field controls the wait for each upstream. Set
it to `0` to use the gonnect default. Saved config stores it in
`attemptTimeoutMS`.

The left-directed `DNS` port publishes the combined `almagest/dns` interface.
The right-directed `slot N` ports accept upstream `almagest/dns` interfaces.
Each slot accepts one link. Empty slots are skipped, and the node supports up to
16 slots.

The node keeps the first unused slot visible. When that slot gets a link, the
node adds the next unused slot. Removing links removes extra empty slots but
does not change the numbers or order of the remaining slots.

The fallback runtime stays stable while inputs change. A timeout change replaces
the fallback runtime but keeps the attached input wrappers. Attach, detach, and
replacement operations affect new requests immediately. A slot change cancels an
in-flight attempt that uses the old input, and the request can continue with
later attached slots. Removing an input closes only the detached wrapper owned
by that graph link; it does not close the upstream node's DNS runtime.
