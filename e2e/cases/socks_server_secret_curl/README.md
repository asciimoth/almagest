# SOCKS server secret with curl

This case starts an Almagest SOCKS server with one authenticated user. The user
entry contains an incorrect inline fallback password and selects a protected
secret with the accepted password.

curl acts as an independent SOCKS5 client and requests a page from a Python HTTP
server through Almagest. The test also confirms that the incorrect inline
password is rejected, which proves that the server uses the selected secret.
