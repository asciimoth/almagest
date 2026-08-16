package networknodes

import (
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

// Classes returns all standard network node classes.
func Classes(runtime nodekit.Runtime) []pasta.NodeClass {
	return []pasta.NodeClass{
		OutNetworkClass{Runtime: runtime},
		MockNetworkClass{},
		NetworkRouterClass{Runtime: runtime},
		RemapperClass{},
		SnifferClass{Runtime: runtime},
		SpooferClass{Runtime: runtime},
	}
}
