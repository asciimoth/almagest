package almagest

import (
	_ "embed"

	"github.com/asciimoth/pasta/pasta"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return udpPingServerClass{engine: e}
	}, true)
	regClass(func(e *engine) pasta.NodeClass {
		return udpPingClientClass{engine: e}
	}, true)
}

const (
	NodeTypeUDPPingServer = "almagest/UDPPingServer"
	NodeTypeUDPPingClient = "almagest/UDPPingClient"

	udpSocketListen = "listen"
	udpSocketDial   = "dial"

	defaultUDPPingNetwork    = "udp4"
	defaultUDPPingHost       = "127.0.0.1"
	defaultUDPPingPort       = 9099
	defaultUDPPingClientPort = 0
	defaultUDPPingPayload    = "ping"
	defaultUDPPingTimeoutMS  = 1000

	udpPingStatusReady   = "ready"
	udpPingStatusRunning = "running..."
	udpPingStatusFail    = "fail"
)

var (
	//go:embed node_debug_udp.md
	udpPingClassLongDescription string
)
