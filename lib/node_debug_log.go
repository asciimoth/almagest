package almagest

import (
	"github.com/asciimoth/pasta/pasta"
)

func init() {
	regClass(func(*engine) pasta.NodeClass {
		return debugLogNetworkClass{}
	}, true)
	regClass(func(e *engine) pasta.NodeClass {
		return debugLogTunClass{engine: e}
	}, true)
}

const (
	NodeTypeDebugLogNetwork = "debug.almagest/LogNetwork"
	NodeTypeDebugLogTun     = "debug.almagest/LogTun"
)
