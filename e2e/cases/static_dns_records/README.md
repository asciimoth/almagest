# Static DNS Records

This hermetic case exercises `StaticDns` configuration through the daemon and
admin API. It does not use the container network for DNS requests.

The graph contains a primary static provider, a fallback static provider, and
one interactive DNS query node.

Assertions cover these situations:

- all supported static record types produce the expected DNS answers;
- names and record types are normalized before a query;
- quoted TXT data and numeric MX and SRV fields round-trip correctly;
- a miss uses the attached fallback provider;
- removing and restoring the fallback link changes miss handling;
- invalid record forms report their precise error and keep the last valid
  provider active;
- field validation reports invalid records and TTL values without applying them;
  and
- valid live edits replace the records and normalize a negative TTL to the
  documented default.
