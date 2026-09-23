package debugnodes

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/pasta/pasta"
)

const NodeTypeDebugMarkdown = "almagest/Markdown"

type DebugMarkdownClass struct{}

func (DebugMarkdownClass) ClassName() string { return NodeTypeDebugMarkdown }
func (DebugMarkdownClass) ShortDescription() string {
	return "DEBUG node demonstrating markdown descriptions"
}
func (DebugMarkdownClass) LongDescription() string {
	return "Text.  \n*Text2*  \n**Text3**  \n[link](http://example.com)  \n`code`  \n```\ncode2\n```"
}
func (DebugMarkdownClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: "", InitialPorts: []pasta.Port{}}
}
func (DebugMarkdownClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &debugMarkdownNode{}, nil
}

// debugMarkdownNode has non-zero size because Pasta uses node pointer identity
// to reject the same implementation instance from being added more than once.
// Go permits pointers to distinct zero-size values to compare equal.
type debugMarkdownNode struct {
	pasta.BasicNode
	_ byte
}
