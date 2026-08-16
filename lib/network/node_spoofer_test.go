package networknodes

import (
	"testing"

	"github.com/asciimoth/gonnect"
	gonnecttun "github.com/asciimoth/gonnect/tun"
)

func TestSpooferNodeStartsRebuildsAndStopsWithInputs(t *testing.T) {
	sourceTun, peerTun := gonnecttun.Pipe(1, defaultSpooferMTU, 0, 0)
	t.Cleanup(func() {
		_ = sourceTun.Close()
		_ = peerTun.Close()
	})
	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	t.Cleanup(func() {
		_ = network.Close()
	})

	node := &spooferNode{
		config:       defaultSpooferConfig(),
		tun:          sourceTun,
		networkValue: network,
	}
	node.config.normalize()

	node.rebuildStack()
	first := node.runtime
	if first == nil {
		t.Fatal("Spoofer runtime = nil, want running stack")
	}

	if err := node.applyConfigForm(spooferRuntimeTestConfigValues()); err != nil {
		t.Fatalf("applyConfigForm: %v", err)
	}
	second := node.runtime
	if second == nil {
		t.Fatal("Spoofer runtime after form apply = nil, want running stack")
	}
	if second == first {
		t.Fatal("Spoofer form apply reused runtime, want rebuilt stack")
	}

	node.networkValue = nil
	node.rebuildStack()
	if node.runtime != nil {
		t.Fatal("Spoofer runtime after network detach != nil, want stopped")
	}
}

func spooferRuntimeTestConfigValues() map[string]any {
	return map[string]any{
		"mtu":                  1300,
		"queueLength":          64,
		"disableSACK":          true,
		"ttl":                  64,
		"icmpBurst":            8,
		"icmpLimit":            1.5,
		"tcpSendBufferSize":    4096,
		"tcpReceiveBufferSize": 4096,
		"congestionControlAlg": "",
		"disableNagle":         true,
		"disableTCPModRecBuff": true,
		"tcpKeepAlive":         true,
		"tcpKeepAliveIdle":     10,
		"tcpKeepAliveInterval": 5,
		"tcpKeepAliveCount":    3,
		"tcpForwardWnd":        1024,
		"tcpForwardAttempts":   16,
	}
}
