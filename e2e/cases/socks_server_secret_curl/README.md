# SOCKS server secret with curl

This case starts an Almagest SOCKS server with one authenticated user. The user
entry contains an incorrect inline fallback password and selects a protected
secret with the accepted password.

curl acts as an independent SOCKS5 client and requests a page from a Python HTTP
server through Almagest. The lifecycle then combines these operations:

- apply an array-backed user configuration through the node menu;
- rotate, rename, delete, and recreate the selected mutable secret;
- reject an invalid listener form without replacing the active runtime;
- replace the listener port and disable and enable the CONNECT command; and
- detach and restore the proxy and listen network links.

Every usable state sends real authenticated traffic. Failure states confirm
that stale passwords, listeners, commands, and network wrappers are not used.
