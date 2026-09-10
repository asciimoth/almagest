# sysnet-linux systemd-resolved compatibility

This privileged case ports the systemd-resolved Docker scenario from
`github.com/asciimoth/sysnet-linux` to `almagestd`. It starts the real D-Bus
service and systemd-resolved. A DefaultTun node installs a managed DNS server,
while OutNetwork keeps a separate original upstream. The two servers return
conflicting answers so the test can detect use of the wrong path.

The test sends system, DNS-backed OutNetwork, hosts-file, `localhost`, and
`NXDOMAIN` requests. It verifies the response and the query log of each DNS
server. systemd-resolved can query eligible links in parallel, so the selected
system answer and managed-server log prove DNS takeover.
