# sysnet-linux Debian resolvconf compatibility

This privileged case uses the real Debian resolvconf implementation. A
DefaultTun node installs a managed DNS server, while OutNetwork keeps a separate
original upstream. The two servers return conflicting answers so the test can
detect use of the wrong path.

The test sends system, DNS-backed OutNetwork, hosts-file, `localhost`, and
`NXDOMAIN` requests. It verifies the response and the query log of each DNS
server.
