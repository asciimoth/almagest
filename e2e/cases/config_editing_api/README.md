# config_editing_api

## Setup

The case starts a real daemon with a `NetworkRouter`, two routed mock HTTP
backends, and a separate stable proxy/server branch. The protected config also
contains a marker secret that must never appear on the config API.

## Actions and Expected Behavior

- Read the editable HuJSON and verify that it does not contain protected data.
- Change only router rules and verify that only the router is reinitialized.
- Verify optimistic revision conflicts and the new routed HTTP behavior.
- Add a three-node proxy, mock-network, and HTTP-server branch through HuJSON.
- Send traffic through the new branch, then delete it through HuJSON.
- Verify that all original node IDs and both stable link IDs stay unchanged.
- Submit an embedded top-level `Protected` object and verify that the API
  rejects it without making the data visible.

The case uses only userspace mock networks and does not require privileged
Docker networking.
