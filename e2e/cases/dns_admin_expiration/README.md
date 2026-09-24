# dns_admin_expiration

## Setup

The case starts a real daemon with temporary storage. A cache-only `DnsCache`
node and a linked `DnsQuery` node use the same named DNS cache. The case uses
only userspace resources and does not require privileged Docker access.

## Actions and Expected Behavior

- Insert a short-lived record through the DNS admin API and resolve it through
  the live `DnsCache` node before it expires.
- Read the expired value through the admin API and verify that the value
  subscription reports deletion.
- Verify that the live cache does not retain the expired in-memory answer.
- Expire another record during a key-list snapshot and verify that the key
  subscription reports deletion.
- Store records with the supported `CH`, `HS`, and numeric DNS classes.
- Reject unsupported classes and malformed MX and SRV values without changing
  the cache.
