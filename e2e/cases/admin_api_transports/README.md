# admin_api_transports

## Setup

The case starts a real daemon with both Unix and web admin listeners. The Unix
listener permits the `root` username, and the web listener permits UID 0. The
web listener also exposes the optional pprof endpoints.

## Actions and Expected Behavior

- Use the Unix listener to inspect API discovery data and the type color map.
- Use the web WebSocket listener for API discovery and ping requests.
- Verify that the web listener serves the admin panel and pprof index.
- Acquire a mutation lease through Unix and verify that a web client sees the
  same active lease.
- Reject renewal with an unknown lease ID.
- Renew and release the lease through the web listener.
- Verify that releasing an unknown ID does not release the active lease.
- Verify default-TTL acquisition and automatic takeover after a short lease
  expires.

The case uses only loopback and userspace admin transports. It does not require
privileged Docker networking.
