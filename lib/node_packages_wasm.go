//go:build wasm

package almagest

import (
	"github.com/asciimoth/pasta/pasta"

	httpnodes "github.com/asciimoth/almagest/lib/http"
	networknodes "github.com/asciimoth/almagest/lib/network"
)

// packageNodeClasses returns the small set of nodes supported by the web demo.
// The debug flag does not add classes on WASM builds.
func packageNodeClasses(runtime *engine, _ bool) []pasta.NodeClass {
	return []pasta.NodeClass{
		httpnodes.HTTPClientClass{Runtime: runtime},
		httpnodes.HTTPServerClass{Runtime: runtime},
		networknodes.MockNetworkClass{},
	}
}
