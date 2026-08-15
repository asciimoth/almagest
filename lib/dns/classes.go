package dnsnodes

import (
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

// Classes returns all standard DNS node classes.
func Classes(runtime nodekit.Runtime) []pasta.NodeClass {
	return []pasta.NodeClass{
		DNS2NetworkClass{Runtime: runtime},
		DNSCacheClass{Runtime: runtime},
		DNSClientClass{Runtime: runtime},
		DNSInjectClass{Runtime: runtime},
		DNSQueryClass{Runtime: runtime},
		DNSRouterClass{Runtime: runtime},
		DNSServerClass{Runtime: runtime},
		MeshNamesClass{Runtime: runtime},
		OutDNSClass{Runtime: runtime},
		StaticDNSClass{Runtime: runtime},
		Tun2DNSClass{Runtime: runtime},
	}
}

// DebugClasses returns DNS node classes that are available in debug mode.
func DebugClasses(runtime nodekit.Runtime) []pasta.NodeClass {
	return []pasta.NodeClass{
		DebugLogDNSClass{Runtime: runtime},
	}
}
