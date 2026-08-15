package almagest

import (
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

type nodeClassConstructor struct {
	isDebug     bool
	constructor func(*engine) pasta.NodeClass
}

var nodeClassConstructors []nodeClassConstructor

func regClass(constructor func(*engine) pasta.NodeClass, isDebug bool) {
	nodeClassConstructors = append(nodeClassConstructors, nodeClassConstructor{
		constructor: constructor,
		isDebug:     isDebug,
	})
}

func nodeClasses(engine *engine, isDebug bool) []pasta.NodeClass {
	// WARN: At init time engine.mainWorkspace is nil
	classes := packageNodeClasses(engine, isDebug)
	defaultTunSupported := engine != nil && engine.sys != nil && engine.sys.Features().DefaultTun
	tunSupported := engine != nil && engine.sys != nil && engine.sys.Features().Tun
	for _, registered := range nodeClassConstructors {
		if registered.isDebug && !isDebug {
			continue
		}
		nodeClass := registered.constructor(engine)
		if nodeClass.ClassName() == NodeTypeDefaultTun && !defaultTunSupported {
			continue
		}
		if nodeClass.ClassName() == NodeTypeTun && !tunSupported {
			continue
		}
		classes = append(classes, nodeClass)
	}

	// Add pasta standard lib classes
	classes = append(classes, std.StdClasses()...)
	return classes
}
