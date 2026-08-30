# Network Firewall with curl and OS HTTP services

This case runs two real Python HTTP services in the Docker container. A real
`curl` process reaches them through a SOCKS listener and this graph:

```text
curl -> LocalProxy -> NetworkFirewall -> OutNetwork -> Python HTTP service
                         ^
                         |
                    ObjectConstant
```

The services listen on `127.0.0.1:18101` and `127.0.0.1:18102`. Direct
requests first prove that both operating-system services are ready. Proxied
requests then prove that both services are initially allowed through the
firewall.

## Policy changes and assertions

1. The attached `ObjectConstant` adds an outgoing TCP block for port `18101`.
   The first service becomes unreachable through SOCKS, while port `18102`
   stays reachable.
2. The firewall menu adds a second outgoing TCP block for port `18102`. Both
   services are now blocked. This also proves that menu and object policies are
   merged.
3. Clearing the object value allows port `18101` again, while the menu rule
   continues to block port `18102`.
4. Clearing the menu rules allows both services again.

All policy changes use the daemon admin API after startup. The graph and its
network wrapper are not rebuilt by the test.

The workspace stores `rules` as a native HJSON object. The admin menu still
accepts JSON text and converts it to that structured form when the workspace
is saved.
