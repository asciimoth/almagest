# config_editing_api

## Setup

The case starts a real daemon with a `NetworkRouter`, two routed mock HTTP
backends, and a separate stable proxy/server branch. The protected config also
contains a marker secret that must never appear on the config API.

## Actions and Expected Behavior

- Read the editable HuJSON and verify that it does not contain protected data.
- Read repeated snapshots and verify that their text and revision stay stable.
- Change only router rules and verify that only the router is reinitialized.
- Verify optimistic revision conflicts and the new routed HTTP behavior.
- Reject malformed and duplicate-link documents without changing the saved
  config, node identities, or live traffic.
- Reject an update that fails after rebuilding a live HTTP server, then verify
  that graph rollback restores the original routes and response.
- Apply and remove node-position metadata without rebuilding nodes or links.
- Rewire a live proxy between mock networks without rebuilding either endpoint,
  verify traffic on each link, and restore the original link.
- Add a three-node proxy, mock-network, and HTTP-server branch through HuJSON.
- Send traffic through the new branch, then delete it through HuJSON.
- Verify that all original node IDs and both stable link IDs stay unchanged.
- Submit an embedded top-level `Protected` object and verify that the API
  rejects it without making the data visible.
- Apply the current canonical document as a graph no-op and verify that its
  text and revision do not change.
- Restart the daemon and verify the saved graph, traffic, and protected-data
  boundary again, then repeat the snapshot-stability check.

The case uses only userspace mock networks and does not require privileged
Docker networking.
