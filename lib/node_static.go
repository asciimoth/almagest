package almagest

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/pasta/pasta"
)

type staticNodeClass struct {
	name             string
	shortDescription string
	longDescription  string
	params           pasta.NodeClassParams
	newNode          func(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error)
}

func (c staticNodeClass) ClassName() string        { return c.name }
func (c staticNodeClass) ShortDescription() string { return c.shortDescription }
func (c staticNodeClass) LongDescription() string  { return c.longDescription }
func (c staticNodeClass) DefaultNodeParams() pasta.NodeClassParams {
	return c.params
}

func (c staticNodeClass) NewNode(cfg configer.Config, states ...*pasta.NodeClassState) (pasta.Node, error) {
	if c.newNode != nil {
		return c.newNode(cfg, states...)
	}
	return &pasta.BasicNode{}, nil
}
