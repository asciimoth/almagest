package almagest

import (
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func nodeClasses(engine *engine, isDebug bool) []pasta.NodeClass {
	// WARN: At init time engine.mainWorkspace is nil
	classes := packageNodeClasses(engine, isDebug)

	// Add pasta standard lib classes
	classes = append(classes, std.StdClasses()...)
	return classes
}
