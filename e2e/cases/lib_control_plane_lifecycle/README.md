# lib_control_plane_lifecycle

## Setup

This hermetic case uses debug-only library nodes, persistent daemon storage,
and one independent in-memory HTTP path. It does not need privileged
networking or an external service.

## Expected Behavior

- Full engine subscriptions publish the color map, workspace snapshot, and
  worker snapshot before later incremental events.
- The `DebugKV` node creates, reads, updates, and deletes values through its
  Formular messages against the same real KV manager as the admin API.
- Default and plugin stores and separate subtrees do not leak into each other.
- Hidden binary data round-trips through the node and admin API. Invalid paths,
  missing plugin names, and invalid base64 do not replace stored values.
- A debug worker panic publishes matching worker-spawned and worker-failed
  events, removes the failed worker, and replaces its parent with an error
  placeholder.
- A panic in a node callback also replaces only that node with an error
  placeholder. The daemon and an unrelated HTTP data plane stay usable.
- Restart reconstructs both failed node implementations and retains KV data.
- Class-wide removal deletes both KV nodes as one batch. One undo and redo
  restore and remove both recorded nodes with their original IDs.
- Many stateless `Markdown` nodes can coexist. Copy/paste, batched undo and redo,
  and daemon restart reconstruct all instances with distinct live records.
  Batch history also preserves the original node IDs.
