package almagest

import (
	"fmt"
	"testing"

	"github.com/asciimoth/formular"
)

func TestRemapperE2ERemapsHTTPClientDestinationPort(t *testing.T) {
	e := newHTTPE2E(t)
	listenPort := httpTestPort()
	publicPort := httpTestPort()

	mock := e.AddNode(NodeTypeMockNetwork, "mock")
	remapper := e.AddNode(NodeTypeRemapper, "remapper")
	server := e.addHTTPServer("server", listenPort)
	client := e.addHTTPClient("client", publicPort)
	e.SubscribeMenus(remapper, server, client)

	e.applyRemapperRules(remapper, fmt.Sprintf("DST PORT %d MATCH 127.0.0.1:%d OP Dial", listenPort, publicPort))
	e.connectByPortName(mock, "left", "Network", remapper, "right", "Network")
	e.connectByPortName(remapper, "left", "Network", client, "right", "Network")
	e.connectByPortName(mock, "left", "Network", server, "right", "Network")

	e.waitServerListening(server, fmt.Sprintf("127.0.0.1:%d", listenPort))
	e.sendClientRequest(client)
	e.waitClientResponse(client, "200 OK", "Debug http server response")
	e.AssertNodeLabel(remapper, "remapping 1 rule")
}

func TestRemapperE2ERemapsHTTPServerListenPort(t *testing.T) {
	e := newHTTPE2E(t)
	configuredPort := httpTestPort()
	listenPort := httpTestPort()

	mock := e.AddNode(NodeTypeMockNetwork, "mock")
	remapper := e.AddNode(NodeTypeRemapper, "remapper")
	server := e.addHTTPServer("server", configuredPort)
	client := e.addHTTPClient("client", listenPort)
	e.SubscribeMenus(remapper, server, client)

	e.applyRemapperRules(remapper, fmt.Sprintf("SRC PORT %d MATCH 127.0.0.1:%d OP Listen", listenPort, configuredPort))
	e.connectByPortName(mock, "left", "Network", remapper, "right", "Network")
	e.connectByPortName(remapper, "left", "Network", server, "right", "Network")
	e.connectByPortName(mock, "left", "Network", client, "right", "Network")

	e.waitServerListening(server, fmt.Sprintf("127.0.0.1:%d", configuredPort))
	e.sendClientRequest(client)
	e.waitClientResponse(client, "200 OK", "Debug http server response")
	e.AssertNodeLabel(remapper, "remapping 1 rule")
}

func (e *httpE2E) applyRemapperRules(node uint64, rules string) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     remapperConfigBlockID,
		Values:      map[string]any{remapperRulesField: rules},
	})
}
