package almagest

import (
	"github.com/asciimoth/pasta/pasta"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return staticNodeClass{
			name:             NodeTypeDebugMarkdwon,
			shortDescription: "DEBUG node demonstrating markdown descriptions",
			longDescription:  "Text.  \n*Text2*  \n**Text3**  \n[link](http://example.com)  \n`code`  \n```\ncode2\n```",
			params:           pasta.NodeClassParams{PrimaryType: "", InitialPorts: []pasta.Port{}},
		}
	}, true)
}

const NodeTypeDebugMarkdwon = "debug.almagest/Markdown"
