package lib

import (
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func nodeClasses(engine *engine) []pasta.NodeClass {
	// WARN: At init time engine.mainWorkspace is nil
	classes := []pasta.NodeClass{
		mockNetClass{},
		httpServerClass{
			engine: engine,
		},
		httpClientClass{
			engine: engine,
		},
	}
	// Add pasta standard lib classes
	classes = append(classes, std.StdClasses()...)
	return classes
}
