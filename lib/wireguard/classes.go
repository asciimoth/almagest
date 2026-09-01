package wireguardnodes

import (
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

// Classes returns all standard WireGuard node classes.
func Classes(runtime nodekit.Runtime) []pasta.NodeClass {
	return []pasta.NodeClass{
		WireGuardClass{Runtime: runtime},
		WGConfigClass{Runtime: runtime},
	}
}
