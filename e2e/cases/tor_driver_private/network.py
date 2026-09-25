# Four directory authorities make the first private consensus reliable. The
# client is a configuration template. test.sh starts only launch phase 1.
Authority = Node(tag="a", authority=1, relay=1)
ExitRelay = Node(tag="r", relay=1, exit=1)
DirectClient = Node(tag="c", client=1, launch_phase=3)

NODES = Authority.getN(4) + ExitRelay.getN(1) + DirectClient.getN(1)

ConfigureNodes(NODES)
