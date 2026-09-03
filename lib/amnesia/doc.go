// Package amnesianodes provides the AmnesiaCtrl node.
//
// The node imports Amnezia service keys, self-hosted guest keys, and native
// WireGuard or AmneziaWG configurations through github.com/asciimoth/wgo/amnesia.
// It owns one tracked peer on a WireGuard device received through a wgctrl
// link. For a service-key import, it sends the attached device's existing
// public key to the service. It does not use wgo's fallback key generator, and
// reconnecting does not rotate the device key. Its network input is only for
// Amnezia control-plane HTTP traffic.
package amnesianodes
