# storage_admin_api

## Setup

The case starts a real daemon with persistent file storage, two mock-network
nodes, and one protected read-only secret. It uses only userspace resources and
does not require privileged Docker access.

## Actions and Expected Behavior

- Exercise KV store, entry, and value subscriptions.
- Create nested default and plugin KV data, including a hidden binary value.
- Reject invalid KV data, kind conflicts, and attempts to drop a store root
  without changing existing values.
- Delete values and recursively drop a subtree.
- Exercise DNS cache, key, and value subscriptions.
- Create every DNS record form accepted by the admin API and verify its
  canonical key and stored value.
- Replace a DNS record with a different type, delete individual records, and
  drop a complete cache.
- Reject invalid DNS records without changing the cache.
- Exercise secret update subscriptions and mutable secret create, allow-list,
  rename, conflict, clear, and delete operations.
- Reject unknown secret nodes and mutations of a protected secret.
- Rename and delete a graph node and verify that mutable and protected secret
  allow lists follow those graph changes.
- Restart the daemon and verify that retained KV, DNS, secret, graph, and
  protected-secret state is reconstructed correctly.

