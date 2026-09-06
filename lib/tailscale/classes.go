package tailscalenodes

import (
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

// Classes returns all standard Tailscale node classes.
func Classes(runtime nodekit.Runtime) []pasta.NodeClass {
	return []pasta.NodeClass{TailscaleCtrlClass{Runtime: runtime}}
}
