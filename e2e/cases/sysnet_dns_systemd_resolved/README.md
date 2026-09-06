# sysnet-linux systemd-resolved compatibility

This privileged case ports the systemd-resolved Docker scenario from
`github.com/asciimoth/sysnet-linux` v0.2.19 to `almagestd`. It starts the real
D-Bus service and systemd-resolved, lets the native system integration detect
them, and verifies that system DNS uses the provider attached to the
DefaultTun node.
