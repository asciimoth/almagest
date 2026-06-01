package lib

import (
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func NodeClasses(engine Engine) []pasta.NodeClass {
	// WARN: At init time engine.mainWorkspace is nil
	classes := []pasta.NodeClass{
		// TODO: Fill it up with our stdlib nodes
	}
	// Add pasta standard lib classes
	classes = append(classes, std.StdClasses()...)
	return classes
}
