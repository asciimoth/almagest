package tunnodes

import (
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

// Classes returns standard TUN node classes supported by runtime.
func Classes(runtime nodekit.Runtime) []pasta.NodeClass {
	classes := []pasta.NodeClass{
		AutoTunSplitterClass{Runtime: runtime},
		DefaultTunClass{Runtime: runtime},
		TunClass{Runtime: runtime},
		TunConfigStripperClass{Runtime: runtime},
		TunCopyClass{Runtime: runtime},
		TunFirewallClass{Runtime: runtime},
		TunJoinerClass{Runtime: runtime},
		TunSplitterClass{Runtime: runtime},
		VTunClass{Runtime: runtime},
	}
	return filterSupportedClasses(runtime, classes)
}

// DebugClasses returns TUN node classes that are available in debug mode.
func DebugClasses(runtime nodekit.Runtime) []pasta.NodeClass {
	return []pasta.NodeClass{
		DebugLogTunClass{Runtime: runtime},
	}
}

func filterSupportedClasses(runtime nodekit.Runtime, classes []pasta.NodeClass) []pasta.NodeClass {
	if runtime == nil {
		return classes
	}
	sys := runtime.System()
	var report sysnet.CapabilityReport
	if sys != nil {
		report = sys.Capabilities()
	}
	defaultTunSupported := sys != nil && systemCanCreate(report, sysnet.TargetDefaultTun)
	tunSupported := sys != nil && systemCanCreate(report, sysnet.TargetTun)
	out := classes[:0]
	for _, class := range classes {
		if class.ClassName() == NodeTypeDefaultTun && !defaultTunSupported {
			continue
		}
		if class.ClassName() == NodeTypeTun && !tunSupported {
			continue
		}
		out = append(out, class)
	}
	return out
}
