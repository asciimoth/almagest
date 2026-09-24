# DNS Example Graph

Runs the graph from `examples/dns.hujson` under the Docker e2e harness with
native Linux DefaultTun support enabled.

The case starts a local `dnsmasq` upstream on `127.0.0.1:5453` and configures
sysnet-linux direct DNS to use it as the fallback upstream. The upstream serves
`example.com` as `93.184.216.34`, while the graph's static DNS nodes serve
`example1.test` and `example2.test`.

Assertions:

- `DnsQuery LN`, `DnsQuery I2`, `DnsQuery ZG`, and `DnsQuery HF` all resolve.
- `LogDNS md` sees only one `example.com A` request after two `DnsQuery LN`
  sends, proving `DnsCache xP` served the repeat request.
- `HttpClient xP` and an external `curl --socks5-hostname 127.0.0.1:1081`
  request both reach `HttpServer LN`.
- External IPv4 and IPv6 DNS packets are intercepted and answered through
  `DefaultTun`, `TunSplitter`, and `Tun2Dns`.
- Removing the right-directed `DNS` links from `DefaultTun BS` and `Tun2Dns LN`
  makes system `dig` queries fail while the internal `DnsQuery` nodes still
  resolve through the graph.
