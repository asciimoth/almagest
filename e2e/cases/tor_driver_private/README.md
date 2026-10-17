# tor_driver_private

This Linux-only case starts a private Chutney Tor network in one container on
an internal Docker network. The internal network has no route to the public
Internet.

The case checks that `TorDriver` discovers Tor from `PATH`, bootstraps through
an `OutNetwork`, carries an HTTP request through the private Tor exit, and
blocks traffic while its outbound link is detached. Reattaching the link must
restore traffic without replacing the Tor process.

The container and `almagestd` run as root. The test inspects `/proc` to confirm
that tor-driver starts Tor with a non-root real, effective, saved, and file
system UID and with no effective or ambient capabilities.
