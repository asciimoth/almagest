// Package amnesianodes provides Amnesia VPN nodes.
//
// Amnesia owns an API client and an ephemeral userspace WireGuard device. Its
// single network input carries control-plane and tunnel traffic. AmnesiaCtrl
// manages one tracked peer on a separate WireGuard node through a wgctrl link.
// Both nodes import profiles through github.com/asciimoth/wgo/amnesia.
package amnesianodes
