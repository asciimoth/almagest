package debugnodes

import (
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

// Classes returns all debug-only node classes.
func Classes(runtime nodekit.Runtime) []pasta.NodeClass {
	return []pasta.NodeClass{
		DebugErrorsWorkersClass{Runtime: runtime},
		DebugKVClass{Runtime: runtime},
		DebugLogNetworkClass{},
		DebugMarkdownClass{},
		UDPPingClientClass{Runtime: runtime},
		UDPPingServerClass{Runtime: runtime},
	}
}
