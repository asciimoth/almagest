package debugnodes

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
