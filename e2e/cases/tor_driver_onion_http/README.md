# tor_driver_onion_http

This Linux-only case starts a private Chutney Tor network on an internal
Docker network. The Docker network has no route to the public Internet.

One `XTorDriver` node publishes a v3 onion service. An Almagest HTTP server is
connected to the service's listen-only Network. A second, independent
`XTorDriver` node provides a client Network to an Almagest HTTP client.

The test waits for onion-service publication, reads the generated onion
address from the service node's status menu, and configures the HTTP server and
client through their normal menus. It checks that:

- almagestd starts two different Tor child processes;
- the service key is created automatically in secrets storage;
- the HTTP client reaches the generated onion address through the client Tor
  node;
- the HTTP response body crosses the onion service; and
- the HTTP server records the request.
