# Automatic VTun MTU migration

VTun configuration now uses `mtu: 0` as automatic mode. It is also the default
for a new VTun. If no positive graph constraint is available, VTun uses 1500 as
a fallback and writes a warning.

A positive VTun MTU is now a ceiling. The runtime can select a smaller value
when a controller, profile, carrier path, or learned PMTU supplies a safer
limit. Existing positive values stay valid, but they no longer force an unsafe
size.

The runtime menu shows the effective MTU and its source. WireGuard and the
managed Amnezia node also show the carrier UDP budget and the selected
constraint. An IPv6 path that cannot support an inner MTU of 1280 reports an
error instead of increasing the value to an unsafe size.

For migration, change an explicit VTun MTU to zero only after all carrier
networks in that graph expose a datagram-size capability. Keep a positive
ceiling for a transport whose limit is not known.

In-place VTun MTU changes require a gonnect-netstack version whose VTun
implements `SetMTU(int) error`. Almagest uses that method when it is available.
With gonnect-netstack v0.6.1, an MTU-only update uses the compatible resource
replacement path.
