//go:build !wasm

package almagest

import (
	"github.com/asciimoth/pasta/pasta"

	amnesianodes "github.com/asciimoth/almagest/lib/amnesia"
	debugnodes "github.com/asciimoth/almagest/lib/debug"
	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	httpnodes "github.com/asciimoth/almagest/lib/http"
	networknodes "github.com/asciimoth/almagest/lib/network"
	socksnodes "github.com/asciimoth/almagest/lib/socks"
	tailscalenodes "github.com/asciimoth/almagest/lib/tailscale"
	tlsnodes "github.com/asciimoth/almagest/lib/tls"
	triggernodes "github.com/asciimoth/almagest/lib/trigger"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
)

// packageNodeClasses is the central registration point for node classes that
// are implemented outside the root package.
func packageNodeClasses(runtime *engine, isDebug bool) []pasta.NodeClass {
	classes := amnesianodes.Classes(runtime)
	classes = append(classes, dnsnodes.Classes(runtime)...)
	classes = append(classes, httpnodes.Classes(runtime)...)
	classes = append(classes, networknodes.Classes(runtime)...)
	classes = append(classes, socksnodes.Classes(runtime)...)
	classes = append(classes, tailscalenodes.Classes(runtime)...)
	classes = append(classes, tlsnodes.Classes(runtime)...)
	classes = append(classes, tunnodes.Classes(runtime)...)
	classes = append(classes, triggernodes.TimeTriggerClass{Runtime: runtime})
	classes = append(classes, wireguardnodes.Classes(runtime)...)
	if isDebug {
		classes = append(classes, debugnodes.Classes(runtime)...)
		classes = append(classes, dnsnodes.DebugClasses(runtime)...)
		classes = append(classes, tunnodes.DebugClasses(runtime)...)
	}
	return classes
}
