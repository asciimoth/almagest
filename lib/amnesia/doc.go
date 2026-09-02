// Package amnesianodes provides the Amnesia WireGuard controller node.
//
// The node imports Amnezia service keys, self-hosted guest keys, and native
// WireGuard or AmneziaWG configurations through github.com/asciimoth/wgo/amnesia.
// It owns one tracked peer on a WireGuard device received through a wgctrl
// link. Its network input is only for Amnezia control-plane HTTP traffic.
package amnesianodes
