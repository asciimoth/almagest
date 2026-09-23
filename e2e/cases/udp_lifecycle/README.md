# udp_lifecycle

## Setup

The case uses one in-memory `MockNetwork`, one `LogNetwork` wrapper, and the
debug UDP ping client and server. It does not need privileged networking or an
external service.

## Initial Daemon Config

- `UDP Network` provides an isolated loopback network.
- `UDP Log` wraps the network and exposes network replacement behavior.
- `UDP Server` listens on `127.0.0.1:19091`.
- `UDP Client` sends `initial-ping` to the server.

## Expected Behavior

- Repeated UDP requests return the exact pong payload.
- Removing and restoring the client, server, or wrapper input link replaces
  closed network resources and restores traffic on the first request.
- Live port, payload, and socket-mode changes restart the required workers and
  preserve traffic.
- A result from an invalidated in-flight request does not replace the result
  from the current request generation.
- Menu-applied UDP configuration survives a daemon restart.

