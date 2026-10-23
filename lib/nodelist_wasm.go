//go:build wasm

package almagest

import "github.com/asciimoth/pasta/pasta"

func nodeClasses(engine *engine, isDebug bool) []pasta.NodeClass {
	// Do not add the Pasta standard library on WASM. The web demo only exposes
	// the explicitly supported package classes.
	return packageNodeClasses(engine, isDebug)
}
