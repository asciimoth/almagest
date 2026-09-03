package amnesianodes

import (
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

// Classes returns all standard AmnesiaCtrl node classes.
func Classes(runtime nodekit.Runtime) []pasta.NodeClass {
	return []pasta.NodeClass{AmnesiaCtrlClass{Runtime: runtime}}
}
