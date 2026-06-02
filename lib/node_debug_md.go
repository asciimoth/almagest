package lib

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/pasta/pasta"
)

const NodeTypeDebugMarkdwon = "debug.almagest/Markdown"

type debugMdClass struct{}

func (debugMdClass) ClassName() string { return NodeTypeDebugMarkdwon }
func (debugMdClass) ShortDescription() string {
	return "DEBUG node demonstrating markdown descriptions"
}
func (debugMdClass) LongDescription() string {
	return "Text.  \n*Text2*  \n**Text3**  \n[link](http://example.com)"
}
func (debugMdClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: "", InitialPorts: []pasta.Port{}}
}
func (debugMdClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &pasta.BasicNode{}, nil
}
