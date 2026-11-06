package socksnodes

import (
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

// Classes returns all standard SOCKS node classes.
func Classes(runtime nodekit.Runtime) []pasta.NodeClass {
	return []pasta.NodeClass{
		LocalProxyClass{Runtime: runtime},
		SocksClientClass{Runtime: runtime},
		SocksServerClass{Runtime: runtime},
		ShadowsocksClientClass{Runtime: runtime},
		ShadowsocksServerClass{Runtime: runtime},
	}
}
