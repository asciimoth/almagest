# Yggdrasil public-peer autopeering and NodeInfo jumper

This privileged case runs one Almagest daemon with two Yggdrasil nodes and one
reference `yggdrasil-go` process. The reference revision matches the
compatibility case: `be5daeba7ad6b9eb3a30a3fa84e58d3962322dbd`.

A local HTTP server supplies a public-peer document for the reference node.
The Almagest client selects it from that document. A second Almagest Yggdrasil
node connects to the reference node and serves HTTP through its TUN. Its
NodeInfo advertises a direct listener. After the client sends HTTP through
the reference peer, the test checks that the jumper adds a direct connection.
It restarts the reference process and checks HTTP again.

Run with `just test-total e2e:yggdrasil_autopeer_jumper`. Docker must permit
TUN creation. The case uses no public Internet peer.
