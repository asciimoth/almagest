# FetchAddr

`almagest/FetchAddr` queries a public connection-information service through an
attached `almagest/network`. This makes the result describe the selected graph
path instead of the daemon host's default route.

The node supports these providers:

- `ifconfig.co` returns the public IP, two-letter country code, city, latitude,
  and longitude.
- `ipify` returns the public IP. The other values use their defaults.

Country codes are normalized to uppercase ISO-style two-letter values. Invalid
or missing country codes and cities become empty strings. Missing coordinates
become `0`. IP addresses are parsed and emitted in canonical form. A response
without a valid IP address fails the check.

Use the menu **Check** button or emit a `pasta/trigger` event to the **Check**
input. A successful check emits all data values before the **Success** trigger.
A failed check resets and emits the default data values before the **Fail**
trigger. Data outputs also send their current cached value when a link is
attached or requests the value.

The TLS config selector controls HTTPS certificate and client policy in the same
way as the HTTP client node. Changing the provider, TLS config, or network
invalidates an in-progress request so a stale completion cannot update the node.
