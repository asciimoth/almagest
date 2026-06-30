package almagest

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/socksgo/protocol"
)

const testNativeNetworkNodeType = "test/NativeNetwork"

type socksServerE2E struct {
	*NodeE2E
}

func TestSocksServerE2EConnect(t *testing.T) {
	e := newNativeSocksServerE2E(t)
	out := e.AddNode(testNativeNetworkNodeType, "native")
	socksPort := httpTestPort()
	httpPort := httpTestPort()
	server := e.addHTTPServer("target", httpPort)
	socks := e.addSocksServer("socks", socksPort, nil)
	e.SubscribeMenus(server, socks)

	e.MustAddTypedLinkByPortName(TypeNetwork, out, "left", "Network", server, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeNetwork, out, "left", "Network", socks, "right", "listen")
	e.MustAddTypedLinkByPortName(TypeNetwork, out, "left", "Network", socks, "right", "proxy")

	e.waitSocksLog(socks, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))
	body := e.waitSocksHTTPGet(socksPort, httpPort, nil)
	if body != "Debug http server response" {
		t.Fatalf("response body = %q, want debug response", body)
	}
	e.AssertDebugLog(NodeTypeSocksServer, "connect")
}

func TestSocksServerE2EUserPassAuth(t *testing.T) {
	e := newNativeSocksServerE2E(t)
	out := e.AddNode(testNativeNetworkNodeType, "native")
	socksPort := httpTestPort()
	httpPort := httpTestPort()
	server := e.addHTTPServer("target", httpPort)
	socks := e.addSocksServer("socks", socksPort, []socksServerUser{
		{id: "user-1", username: "alice", password: "secret"},
		{id: "user-2", username: "alice", password: "alternate"},
	})
	e.SubscribeMenus(server, socks)

	e.MustAddTypedLinkByPortName(TypeNetwork, out, "left", "Network", server, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeNetwork, out, "left", "Network", socks, "right", "listen")
	e.MustAddTypedLinkByPortName(TypeNetwork, out, "left", "Network", socks, "right", "proxy")
	e.waitSocksLog(socks, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))

	e.waitSocksHTTPGetFailure(socksPort, httpPort, (&protocol.AuthMethods{}).Add(&protocol.PassAuthMethod{User: "alice", Pass: "wrong"}))
	body := e.waitSocksHTTPGet(socksPort, httpPort, (&protocol.AuthMethods{}).Add(&protocol.PassAuthMethod{User: "alice", Pass: "secret"}))
	if body != "Debug http server response" {
		t.Fatalf("response body = %q, want debug response", body)
	}
	body = e.waitSocksHTTPGet(socksPort, httpPort, (&protocol.AuthMethods{}).Add(&protocol.PassAuthMethod{User: "alice", Pass: "alternate"}))
	if body != "Debug http server response" {
		t.Fatalf("response body = %q, want debug response", body)
	}
}

func TestSocksServerFormRejectsSamePlainAndWebSocketAddress(t *testing.T) {
	e := newSocksServerE2E(t)
	socksPort := httpTestPort()
	socks := e.addSocksServer("socks", socksPort, nil)
	e.SubscribeMenus(socks)

	e.Engine.SendNodeFormularMsg(E2EWorkspace, socks, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, socks),
		BlockID:     "config",
		Values: map[string]any{
			"socksHost": "127.0.0.1",
			"socksPort": socksPort,
			"wsHost":    "127.0.0.1",
			"wsPort":    socksPort,
			"wsPath":    "/ws",
		},
	})

	Eventually(t, func() bool {
		item, ok := e.MenuItem(socks, "config", "socksPort")
		return ok && item.Field != nil && item.Status == formular.StatusError
	}, "same listen address validation")
	e.AssertNodeLabel(socks, fmt.Sprintf("127.0.0.1:%d", socksPort))
}

func TestSocksServerFieldValidationReportsStatus(t *testing.T) {
	e := newSocksServerE2E(t)
	socksPort := httpTestPort()
	socks := e.addSocksServer("socks", socksPort, nil)
	e.SubscribeMenus(socks)

	e.Engine.SendNodeFormularMsg(E2EWorkspace, socks, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{
			Type:            formular.MessageFieldValidate,
			MenuID:          pasta.NodeMenuID(socks),
			MenuGeneration:  1,
			BlockGeneration: 1,
		},
		Field: formular.FieldRef{BlockID: "config", FieldID: "socksPort"},
		Value: socksPort,
	})

	Eventually(t, func() bool {
		item, ok := e.MenuItem(socks, "config", "socksPort")
		return ok && item.Field != nil && item.Status == formular.StatusOK
	}, "socks port validation status")
}

func newSocksServerE2E(t *testing.T) *socksServerE2E {
	t.Helper()
	return &socksServerE2E{NodeE2E: NewNodeE2E(t)}
}

func newNativeSocksServerE2E(t *testing.T) *socksServerE2E {
	t.Helper()
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logf,
		newE2EDebugSystem(),
		[]pasta.NodeClass{testNativeNetworkClass{}},
		pool,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})
	e := &NodeE2E{T: t, Engine: engine, LogFactory: logf, menus: formular.NewMenuSnapshotState()}
	sub := engine.SubscribeNotifications(func(notification EngineNotification) {
		if notification.Kind == NotificationWorkspace && notification.Workspace != nil && notification.Workspace.Kind == pasta.NotificationNodeMenu {
			e.menus.Apply(notification.Workspace.Formular)
		}
	})
	t.Cleanup(func() { engine.UnsubscribeNotifications(sub) })
	return &socksServerE2E{NodeE2E: e}
}

type testNativeNetworkClass struct{}

func (testNativeNetworkClass) ClassName() string        { return testNativeNetworkNodeType }
func (testNativeNetworkClass) ShortDescription() string { return "Native network" }
func (testNativeNetworkClass) LongDescription() string  { return "Test-only native network source." }
func (testNativeNetworkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{NetworkPort("left", "Network")}}
}
func (testNativeNetworkClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &testNativeNetworkNode{}, nil
}

type testNativeNetworkNode struct {
	pasta.BasicNode
	w    *pasta.Workspace
	id   uint64
	port uint64
	net  gonnect.Network
}

func (n *testNativeNetworkNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.net = gonnect.DefaultNetwork(nil)
	if restored != nil && len(restored.LeftPorts) > 0 {
		n.port = restored.LeftPorts[0]
	}
	if err := n.w.SetNodePrimary(n.id, TypeNetwork); err != nil {
		return err
	}
	return n.w.SetNodeLabel(n.id, "native")
}

func (n *testNativeNetworkNode) OnReady() error {
	n.sendAll()
	return nil
}

func (n *testNativeNetworkNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.port || portDirection != "left" || linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *testNativeNetworkNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.port && portDirection == "left" && linkType == TypeNetwork {
		n.sendToLink(link)
	}
	return nil
}

func (n *testNativeNetworkNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.port && receiverPortDirection == "left" && linkType == TypeNetwork {
		n.sendToLink(event.Link)
	}
	return nil
}

func (n *testNativeNetworkNode) sendAll() {
	snapshot, ok := n.w.PortSnapshot(n.port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		n.sendToLink(link)
	}
}

func (n *testNativeNetworkNode) sendToLink(link uint64) {
	wrapper := gonnect.DetachNetwork(n.net, nil)
	BindNetworkResource(n.w, n.id, link, wrapper)
	n.w.EmitEvent(n.id, link, NetworkPayload{Network: wrapper})
}

func (e *socksServerE2E) addHTTPServer(name string, port int) uint64 {
	e.T.Helper()
	id := e.AddNode(NodeTypeHTTPServer, name)
	e.Engine.SendNodeFormularMsg(E2EWorkspace, id, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, id),
		BlockID:     "listen",
		Values: map[string]any{
			"host": "127.0.0.1",
			"port": port,
		},
	})
	e.WaitNodeLabel(id, fmt.Sprintf("127.0.0.1:%d", port))
	return id
}

func (e *socksServerE2E) addSocksServer(name string, port int, users []socksServerUser) uint64 {
	e.T.Helper()
	id := e.AddNode(NodeTypeSocksServer, name)
	values := map[string]any{
		"socksHost": "127.0.0.1",
		"socksPort": port,
		"wsHost":    "",
		"wsPort":    0,
		"wsPath":    "/socks",
	}
	if users != nil {
		values["users"] = socksServerUserValues(users)
	}
	e.Engine.SendNodeFormularMsg(E2EWorkspace, id, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, id),
		BlockID:     "config",
		Values:      values,
	})
	e.WaitNodeLabel(id, fmt.Sprintf("127.0.0.1:%d", port))
	return id
}

func (e *socksServerE2E) waitSocksLog(socks uint64, wantPart string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		item, ok := e.MenuItem(socks, "logs", "worker")
		if !ok {
			return false
		}
		for _, line := range item.Logs {
			if strings.Contains(line.Text, wantPart) {
				return true
			}
		}
		return false
	}, "socks log containing %q", wantPart)
}

func (e *socksServerE2E) waitSocksHTTPGet(socksPort, httpPort int, auth *protocol.AuthMethods) string {
	e.T.Helper()
	var body string
	var err error
	Eventually(e.T, func() bool {
		body, err = socksHTTPGet(socksPort, httpPort, auth)
		return err == nil
	}, "socks HTTP GET via %d to %d, last error: %v", socksPort, httpPort, err)
	return body
}

func (e *socksServerE2E) waitSocksHTTPGetFailure(socksPort, httpPort int, auth *protocol.AuthMethods) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		_, err := socksHTTPGet(socksPort, httpPort, auth)
		return err != nil && !strings.Contains(err.Error(), "connection refused")
	}, "socks HTTP GET failure via %d to %d", socksPort, httpPort)
}

func socksServerUserValues(users []socksServerUser) []formular.ArrayElementValue {
	out := make([]formular.ArrayElementValue, 0, len(users))
	for _, user := range users {
		out = append(out, formular.ArrayElementValue{
			ID:       user.id,
			Template: "user",
			Values: map[string]any{
				"username": user.username,
				"password": user.password,
			},
		})
	}
	return out
}

func socksHTTPGet(socksPort, httpPort int, auth *protocol.AuthMethods) (string, error) {
	client := &socksgo.Client{
		SocksVersion: "5",
		ProxyAddr:    fmt.Sprintf("127.0.0.1:%d", socksPort),
		Auth:         auth,
		Filter:       func(_, _ string) bool { return false },
	}
	conn, err := client.Dial(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", httpPort))
	if err != nil {
		return "", err
	}
	defer conn.Close() //nolint:errcheck
	if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nConnection: close\r\n\r\n", httpPort); err != nil {
		return "", err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
