Runs a SOCKS4/SOCKS4a/SOCKS5 proxy server using separate network inputs for client listening and requested proxy operations.

Support
- socks4/sock4a/socks5
- socks over tls
- socks over websocket
- gost extensions
- tor extensions

UDP proxy commands are executed through the connected proxy Network port.
For plain SOCKS5 UDP ASSOCIATE, the server chooses the UDP relay port returned
to the client; the protocol does not let a client reserve its own relay port.
Use gost UDP tunnel support when a UDP application needs stable externally
configured endpoint ports, such as WireGuard peers with fixed endpoints.
