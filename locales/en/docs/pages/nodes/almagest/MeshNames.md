Resolves mesh-specific names. Connect the [DNS](@port/left/DNS) output to
applications or to another DNS node. The node can decode direct name formats
without a [Network](@port/right/Network). Connect [Network](@port/right/Network)
only when you must resolve a format that sends an authority DNS request.

The node supports these name formats:

- `.meshname` uses the label before the suffix as a
  [Meshname](https://github.com/zhoreeq/meshname) authority. The node sends a
  DNS request for the full name to that authority.
- `.meship` decodes its single name label directly to an IPv6 address. This
  format does not send a DNS request.
- `.pk.ygg` derives a [Yggdrasil](https://yggdrasil-network.github.io/) IPv6
  address from the [Ed25519](https://www.rfc-editor.org/rfc/rfc8032) public key
  in the label before the suffix. This format does not send a DNS request.
- `.ygg` uses the label before the suffix as a Yggdrasil authority. Straight
  hexadecimal, base32, and dashed
  [YggNS](https://github.com/ru-crypto-anarchy/YggNS) authority labels are
  supported. The node sends a DNS request for the full name to that authority.
- `.ipv6-literal.net` decodes an IPv6 address from the name. It accepts the
  Windows IPv6 literal form and validates an optional zone identifier. DNS
  answers contain the address without the zone. This format does not send a DNS
  request.

For example, `service.aiag7sesed2aaxgcgbnevruwpy.meshname` sends a request to
the authority identified by `aiag7sesed2aaxgcgbnevruwpy`. The
`aiag7sesed2aaxgcgbnevruwpy.meship` form returns that authority address
directly.

Authority requests use TCP port 53 through the connected
[Network](@port/right/Network). Make sure that the
[Network](@port/right/Network) can reach the derived mesh IPv6 addresses.
Without a [Network](@port/right/Network), `.meshname` and `.ygg` requests fail.
The direct `.meship`, `.pk.ygg`, and `.ipv6-literal.net` formats continue to
work.

Connect [Fallback DNS](@port/right/Fallback%20DNS) to resolve names that are not
recognized as mesh names. Without this connection, these names return a
not-found response. The node does not use a DNS resolver from the
[Network](@port/right/Network) as an implicit fallback. Disconnecting
[Fallback DNS](@port/right/Fallback%20DNS) stops ordinary-name forwarding but
keeps mesh-name resolution available. Disconnecting
[Network](@port/right/Network) replaces the [DNS](@port/left/DNS) output with a
local-only resolver.

The menu has one **Disable** control for each supported name format. All formats
are enabled by default. A disabled format is treated as an unrecognized name.
Thus, a disabled format goes to [Fallback DNS](@port/right/Fallback%20DNS) when
a fallback is connected, or returns a not-found response when no fallback is
connected. The `.ygg` and `.pk.ygg` controls are independent.

**Request timeout (ms)** limits one DNS exchange with a mesh authority. A value
of `0` uses the default timeout of 5 seconds. This timeout does not apply to
direct address decoding or to [Fallback DNS](@port/right/Fallback%20DNS)
requests.

The [DNS](@port/left/DNS) output supports A, AAAA, PTR, CNAME, TXT, MX, NS, and
SRV questions. Successful answers have a TTL of 60 seconds. Unsupported DNS
question types return a not-implemented response.
