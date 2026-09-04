package httpnodes

import (
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

// Classes returns all standard HTTP node classes.
func Classes(runtime nodekit.Runtime) []pasta.NodeClass {
	return []pasta.NodeClass{
		FetchAddrClass{Runtime: runtime},
		HTTPClientClass{Runtime: runtime},
		HTTPServerClass{Runtime: runtime},
	}
}
