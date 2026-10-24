//go:build wasm

package almagest

import (
	"slices"
	"testing"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	httpnodes "github.com/asciimoth/almagest/lib/http"
	networknodes "github.com/asciimoth/almagest/lib/network"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestWASMNodeClasses(t *testing.T) {
	want := []string{
		dnsnodes.NodeTypeDns2Network,
		dnsnodes.NodeTypeDnsCache,
		dnsnodes.NodeTypeDNSClient,
		dnsnodes.NodeTypeDnsFallback,
		dnsnodes.NodeTypeDnsInject,
		dnsnodes.NodeTypeDNSQuery,
		dnsnodes.NodeTypeDnsRouter,
		dnsnodes.NodeTypeDNSServer,
		dnsnodes.NodeTypeMeshNames,
		dnsnodes.NodeTypeStaticDNS,
		dnsnodes.NodeTypeTun2Dns,
		httpnodes.NodeTypeHTTPClient,
		httpnodes.NodeTypeHTTPServer,
		networknodes.NodeTypeMockNetwork,
		networknodes.NodeTypeNetworkFirewall,
		networknodes.NodeTypeNetworkRouter,
		networknodes.NodeTypeRemapper,
		networknodes.NodeTypeSniffer,
		networknodes.NodeTypeSpoofer,
		tunnodes.NodeTypeAutoTunSplitter,
		tunnodes.NodeTypeTunCopy,
		tunnodes.NodeTypeTunFirewall,
		tunnodes.NodeTypeTunJoiner,
		tunnodes.NodeTypeTunSplitter,
		tunnodes.NodeTypeVTun,
		wireguardnodes.NodeTypeWireGuard,
		wireguardnodes.NodeTypeWGConfig,
		std.NodeTypeStringConstant,
		std.NodeTypeObjectConstant,
	}

	for _, debug := range []bool{false, true} {
		classes := nodeClasses(&engine{}, debug)
		got := make([]string, 0, len(classes))
		for _, class := range classes {
			got = append(got, class.ClassName())
		}
		if !slices.Equal(got, want) {
			t.Fatalf("nodeClasses(debug=%t) = %v, want %v", debug, got, want)
		}
	}
}
