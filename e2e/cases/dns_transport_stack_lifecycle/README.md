# DNS Transport Stack Lifecycle

This hermetic case runs a DNS request through a complete userspace stack:

```text
StaticDns -> LogDNS -> DnsServer -> LogNetwork -> MockNetwork
                                              -> LogNetwork -> DnsClient -> DnsCache
                                                                            |
MockNetwork -> DnsInject <---------------------------------------------------+
                  |       |
                  |       +-> Dns2Network -> DnsQuery
                  +-> HttpClient and LocalProxy -> HttpServer
```

It uses two isolated `MockNetwork` instances. One carries UDP DNS traffic. The
other carries HTTP traffic and receives DNS through `DnsInject`. No host DNS or
privileged networking is required.

Assertions cover these situations:

- the first DNS, HTTP, and SOCKS requests work after nodes report ready;
- independent packet logging wrappers observe both sides of the UDP transport;
- two DNS consumers receive independent wrappers from one cache;
- a repeated lookup is served by `DnsCache` without reaching `LogDNS` again;
- removing the DNS server upstream keeps cached data usable but rejects a new
  lookup, and restoring the link recovers the same server listener;
- replacing the static provider below `LogDNS` propagates through the live
  server, client, cache, injector, and adapter stack;
- invalid DNS server and client form edits keep the last valid data plane
  usable, while later valid edits recover their normal labels;
- removing and restoring the DNS client network, the injector DNS input, and the
  injector network input invalidates old wrappers and restores traffic;
- replacing the client logger upstream publishes a usable packet wrapper;
- removing one injector output affects only that consumer;
- a live DNS server port change fails through the old client destination and
  recovers after the client is updated; and
- the edited stack and its runtime resources survive daemon restart.
