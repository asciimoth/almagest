package almagest

import (
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

type nodeClassConstructor struct {
	isDebug     bool
	constructor func(*engine) pasta.NodeClass
}

// This list is populated by init functions via regClass()
var nodeClassConstructors = []nodeClassConstructor{}

func regClass(constructor func(*engine) pasta.NodeClass, isDebug bool) {
	nodeClassConstructors = append(nodeClassConstructors, nodeClassConstructor{
		constructor: constructor,
		isDebug:     isDebug,
	})
}

func nodeClasses(engine *engine, isDebug bool) []pasta.NodeClass {
	// WARN: At init time engine.mainWorkspace is nil
	classes := make([]pasta.NodeClass, 0, len(nodeClassConstructors))
	defaultTunSupported := engine != nil && engine.sys != nil && engine.sys.Features().DefaultTun
	for _, class := range nodeClassConstructors {
		if class.isDebug && !isDebug {
			continue
		}
		nodeClass := class.constructor(engine)
		if nodeClass.ClassName() == NodeTypeDefaultTun && !defaultTunSupported {
			continue
		}
		classes = append(classes, nodeClass)
	}

	// Add pasta standard lib classes
	classes = append(classes, std.StdClasses()...)
	return classes
}
