# graph_history_runtime

## Setup

The case uses one in-memory `MockNetwork`, an HTTP server, and an HTTP client.
It does not need privileged networking or an external service.

## Initial Daemon Config

- `History Network` provides an isolated loopback network.
- `History Server` listens on `127.0.0.1:8080` in that network.
- `History Client` requests the server through the same network.

## Expected Behavior

- The first request after startup succeeds.
- Link removal, undo, redo, and a second undo update the client runtime and do
  not leave it attached to a stale network wrapper.
- Copying and pasting the complete three-node graph preserves its internal
  links. The pasted HTTP runtime works independently of the original graph.
- Link history and server-node history also recover the pasted data plane.
- Copying only the client does not copy its external network link. Undo removes
  that pasted node and redo restores the same disconnected state.
- A node rename stays in place while position undo and redo work without
  interrupting the original HTTP data plane.
