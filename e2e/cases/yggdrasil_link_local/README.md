# Yggdrasil link-local discovery through daemon network links

This privileged case creates a Linux veth pair in the Docker container. Two
Yggdrasil nodes use separate veth interfaces and the same OutNetwork provider.
Neither node has a configured peer URL. Both use multicast to find the other
node over link-local IPv6.

The case runs in isolated and direct daemon modes. It checks both peer
connections and an HTTP request through the two Yggdrasil TUN links. It then
detaches and reattaches one node's OutNetwork link and checks discovery and HTTP
again. This exercises both daemon carrier paths and the final Linux TCP bind and
dial paths.

Run only this case with `just test-total e2e:yggdrasil_link_local`.
