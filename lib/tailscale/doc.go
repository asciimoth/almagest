// Package tailscalenodes provides Tailscale control-plane nodes.
//
// TailscaleCtrl attaches wgo-tailscale to a shared WireGuard device through an
// almagest/wgctrl link. Its Network input carries control, discovery, STUN,
// DISCO, and DERP traffic. Direct peers use the WireGuard node's default UDP
// transport. The node also publishes MagicDNS and ACL-derived firewall rules.
package tailscalenodes
