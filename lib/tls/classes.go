package tlsnodes

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/pasta/pasta"
)

const (
	// NodeTypeTLSWrapper is the node class name for bidirectional TLS
	// client/server wrapping.
	NodeTypeTLSWrapper = "almagest/TlsWrapper"
	// NodeTypeTLSMITM is the node class name for outbound TLS MITM
	// interception.
	NodeTypeTLSMITM = "almagest/TlsMitm"
	// NodeTypeTLSTerminator is the node class name for outbound TLS
	// termination to plaintext TCP.
	NodeTypeTLSTerminator = "almagest/TlsTerminator"
)

// Classes returns all standard TLS node classes.
func Classes(runtime Runtime) []pasta.NodeClass {
	return []pasta.NodeClass{
		TLSWrapperClass{Runtime: runtime},
		TLSMITMClass{Runtime: runtime},
		TLSTerminatorClass{Runtime: runtime},
	}
}

type TLSWrapperClass struct {
	Runtime Runtime
}

func (TLSWrapperClass) ClassName() string { return NodeTypeTLSWrapper }
func (TLSWrapperClass) ShortDescription() string {
	return "TLS client/server network wrapper"
}
func (TLSWrapperClass) LongDescription() string {
	return "Wraps TCP dials in client TLS and TCP listeners in server TLS."
}
func (TLSWrapperClass) DefaultNodeParams() pasta.NodeClassParams {
	return networkMiddlewareParams()
}
func (c TLSWrapperClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return newTLSWrapperNode(c.Runtime, cfg), nil
}

type TLSMITMClass struct {
	Runtime Runtime
}

func (TLSMITMClass) ClassName() string { return NodeTypeTLSMITM }
func (TLSMITMClass) ShortDescription() string {
	return "TLS interception network"
}
func (TLSMITMClass) LongDescription() string {
	return "Intercepts selected outbound TLS dials with the local TLS CA."
}
func (TLSMITMClass) DefaultNodeParams() pasta.NodeClassParams {
	return networkMiddlewareParams()
}
func (c TLSMITMClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return newTLSMITMNode(c.Runtime, cfg), nil
}

type TLSTerminatorClass struct {
	Runtime Runtime
}

func (TLSTerminatorClass) ClassName() string { return NodeTypeTLSTerminator }
func (TLSTerminatorClass) ShortDescription() string {
	return "TLS terminating network"
}
func (TLSTerminatorClass) LongDescription() string {
	return "Terminates outbound TLS dials with the local TLS CA and sends plaintext TCP upstream."
}
func (TLSTerminatorClass) DefaultNodeParams() pasta.NodeClassParams {
	return networkMiddlewareParams()
}
func (c TLSTerminatorClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return newTLSTerminatorNode(c.Runtime, cfg), nil
}

func networkMiddlewareParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
		NetworkPort("right", "Network"),
	}}
}
