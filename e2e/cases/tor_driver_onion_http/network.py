# Four directory authorities make the first consensus reliable. Three more
# relays follow Chutney's hs-v3-min topology and supply the independent paths
# required for introduction and rendezvous circuits. The client is a
# configuration template only.
Authority = Node(tag="a", authority=1, relay=1)
NonExitRelay = Node(tag="r", relay=1)
DirectClient = Node(tag="c", client=1, launch_phase=3)

NODES = (
    Authority.getN(4)
    + NonExitRelay.getN(3)
    + DirectClient.getN(1)
)

ConfigureNodes(NODES)
