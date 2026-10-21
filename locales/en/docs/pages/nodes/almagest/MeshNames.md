Resolves mesh-specific names through a connected Network. Connect the DNS output
to applications or to another DNS node. The node provides no DNS output until
the Network is ready.

The node supports these name formats:

- `.meshname` uses the label before the suffix as a Meshname authority. The node
  sends a DNS request for the full name to that authority.
- `.meship` decodes its single name label directly to an IPv6 address. This
  format does not send a DNS request.
- `.pk.ygg` derives a Yggdrasil IPv6 address from the Ed25519 public key in the
  label before the suffix. This format does not send a DNS request.
- `.ygg` uses the label before the suffix as a Yggdrasil authority. Straight
  hexadecimal, base32, and dashed YggNS authority labels are supported. The node
  sends a DNS request for the full name to that authority.

For example, `service.aiag7sesed2aaxgcgbnevruwpy.meshname` sends a request to
the authority identified by `aiag7sesed2aaxgcgbnevruwpy`. The
`aiag7sesed2aaxgcgbnevruwpy.meship` form returns that authority address
directly.

Authority requests use TCP port 53 through the connected Network. Make sure that
the Network can reach the derived mesh IPv6 addresses. The Network is also
required to activate the node when you use only the direct `.meship` or
`.pk.ygg` formats.

Connect **Fallback DNS** to resolve names that are not recognized as mesh names.
Without this connection, these names return a not-found response. The node does
not use a DNS resolver from the Network as an implicit fallback. Disconnecting
**Fallback DNS** stops ordinary-name forwarding but keeps mesh-name resolution
available. Disconnecting Network removes the DNS output.

**Request timeout (ms)** limits one DNS exchange with a mesh authority. A value
of `0` uses the default timeout of 5 seconds. This timeout does not apply to
direct address decoding or to **Fallback DNS** requests.

The DNS output supports A, AAAA, PTR, CNAME, TXT, MX, NS, and SRV questions.
Successful answers have a TTL of 60 seconds. Unsupported DNS question types
return a not-implemented response.
