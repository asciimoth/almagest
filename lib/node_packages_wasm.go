//go:build wasm

package almagest

import (
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	httpnodes "github.com/asciimoth/almagest/lib/http"
	networknodes "github.com/asciimoth/almagest/lib/network"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
)

// packageNodeClasses returns the node classes supported by the web demo.
// The debug flag does not add classes on WASM builds.
func packageNodeClasses(runtime *engine, _ bool) []pasta.NodeClass {
	return []pasta.NodeClass{
		dnsnodes.DNS2NetworkClass{Runtime: runtime},
		dnsnodes.DNSCacheClass{Runtime: runtime},
		dnsnodes.DNSClientClass{Runtime: runtime},
		dnsnodes.DNSFallbackClass{Runtime: runtime},
		dnsnodes.DNSInjectClass{Runtime: runtime},
		dnsnodes.DNSQueryClass{Runtime: runtime},
		dnsnodes.DNSRouterClass{Runtime: runtime},
		dnsnodes.DNSServerClass{Runtime: runtime},
		dnsnodes.MeshNamesClass{Runtime: runtime},
		dnsnodes.StaticDNSClass{Runtime: runtime},
		dnsnodes.Tun2DNSClass{Runtime: runtime},
		httpnodes.HTTPClientClass{Runtime: runtime},
		httpnodes.HTTPServerClass{Runtime: runtime},
		networknodes.MockNetworkClass{},
		networknodes.NetworkFirewallClass{},
		networknodes.NetworkRouterClass{Runtime: runtime},
		networknodes.RemapperClass{},
		networknodes.SnifferClass{Runtime: runtime},
		networknodes.SpooferClass{Runtime: runtime},
		tunnodes.AutoTunSplitterClass{Runtime: runtime},
		tunnodes.TunCopyClass{Runtime: runtime},
		tunnodes.TunFirewallClass{Runtime: runtime},
		tunnodes.TunJoinerClass{Runtime: runtime},
		tunnodes.TunSplitterClass{Runtime: runtime},
		tunnodes.VTunClass{Runtime: runtime},
		wireguardnodes.WireGuardClass{Runtime: runtime},
		wireguardnodes.WGConfigClass{Runtime: runtime},
		std.StringConstantClass{},
		std.ObjectConstantClass{},
	}
}
