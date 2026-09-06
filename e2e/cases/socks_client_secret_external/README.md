# SOCKS client secret with an external server

This case starts MicroSocks as an independent SOCKS5 server with username and
password authentication. The Almagest graph connects an HTTP client through a
SOCKS client node to MicroSocks and then to a Python HTTP server.

The SOCKS client URL contains an incorrect fallback password. Its selected
protected secret contains the password accepted by MicroSocks. The test passes
only when Almagest replaces the parsed URL password with the secret value.
