Sends DNS requests to upstream servers through an attached Network. Connect the
transport network to **Network** and connect DNS consumers to **DNS**. The DNS
output becomes available when the Network is ready and at least one valid
server is configured.

## DNS servers

Add upstreams to **DNS servers** in the order that you want the node to try
them. Each server URL must use one of these schemes:

- `udp://` sends DNS over UDP. If the server returns a truncated response, the
  node retries the request over TCP to the same address.
- `tcp://` sends DNS over TCP.
- `dot://` sends DNS over TLS.

For example, use `udp://192.0.2.53:53`, `tcp://192.0.2.53:53`, or
`dot://dns.example:853`. An address without a scheme becomes a UDP URL. If you
omit the port, the node uses port 53 for all three schemes. Specify port 853
when a DNS-over-TLS server uses the standard DoT port.

A server URL cannot contain user information, a query, a fragment, or
whitespace in its address.

The node tries server URLs in list order after a connection or protocol error.
It returns the first valid DNS response. A DNS response code such as `NXDOMAIN`
or `SERVFAIL` is a valid response, so it does not cause the node to try the next
server.

## Bootstrap DNS

Connect **Bootstrap DNS** when a server URL contains a hostname. The bootstrap
resolver converts that hostname to IP addresses before the node opens the
upstream connection. A server URL that contains an IP address does not need
bootstrap DNS. The Network supplies the connection path, but it does not
resolve server hostnames for this node.

For UDP and TCP, the node tries bootstrap addresses in resolver order. For
DNS over TLS, it tries all returned addresses in parallel and uses the first
valid response.

## TLS certificates

The selected **TLS config** applies only to `dot://` servers. It controls the
trusted certificates and certificate-verification policy. Without an explicit
verification name in that config, the node verifies the certificate against
the host in the server URL. This rule also applies when bootstrap DNS connects
the node to a resolved IP address. For an IP address in the URL, the
certificate must contain that IP address unless the TLS config supplies a
different verification name.

## Request timeout

**Request timeout (ms)** limits one complete request. The limit includes
bootstrap resolution, attempts against the configured servers, connection
setup, the TLS handshake, and network reads and writes. Set the value to `0`
to use the default timeout of 5 seconds. Negative values are not valid.

Changing the Network, Bootstrap DNS, server list, TLS config, or timeout closes
the current DNS output and cancels requests that use it. The node supplies a
new DNS output when the new configuration is ready.
