# Managed Amnesia self-host service

This privileged case runs a local encrypted Amnezia API-v2 gateway and an
`amneziawg-go` peer. The `almagest/Amnesia` node owns its WireGuard device and
uses one outgoing Network link for the gateway HTTPS request and tunnel UDP.

The case verifies:

- service import with a client-generated WireGuard key;
- profile address and DNS propagation to VTun;
- bidirectional traffic through the managed AmneziaWG tunnel;
- a new client key after a secret change, network reattach, TLS recovery, and
  daemon restart;
- installation UUID persistence across daemon restart;
- the reduced managed-node menu and the absence of WireGuard configuration.

The fixture disables random handshake trailers because their 65535-byte
maximum cannot fit in the 65507-byte IPv4 UDP payload limit. The dedicated WGO
compatibility case tests random-trailer interoperability.

The generated client profile uses a fixed loopback listen port. The mock
server learns the generated public key only after import, so the fixed endpoint
lets it initiate the first usable handshake without a client retransmission
race.

Run it with:

```sh
E2E_DOCKER_RUN_ARGS=--privileged ./e2e/run.sh amnesia_selfhost_managed
```
