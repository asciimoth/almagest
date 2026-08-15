package almagest

import (
	"github.com/asciimoth/pasta/pasta"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
)

// packageNodeClasses is the central registration point for node classes that
// are implemented outside the root package.
func packageNodeClasses(runtime *engine, isDebug bool) []pasta.NodeClass {
	classes := dnsnodes.Classes(runtime)
	if isDebug {
		classes = append(classes, dnsnodes.DebugClasses(runtime)...)
	}
	return classes
}
