package almagest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestHTTPRequestUsesTLSConfig(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	network := gonnect.DetachNetwork(gonnect.DefaultNetwork(nil), nil, nil)
	t.Cleanup(func() { _ = network.Close() })

	result := doHTTPRequest(context.Background(), httpClientRequest{
		id:      1,
		network: network,
		url:     server.URL,
		method:  http.MethodGet,
		tlsConfig: &tls.Config{
			RootCAs:    roots,
			MinVersion: tls.VersionTLS12,
		},
	})
	if result.err != "" || result.status != "200 OK" || result.body != "ok" {
		t.Fatalf("HTTPS result = %#v, want 200 OK body ok", result)
	}
}

func TestHTTPRequestReportsTLSConfigErrorForHTTPS(t *testing.T) {
	network := gonnect.DetachNetwork(gonnect.DefaultNetwork(nil), nil, nil)
	t.Cleanup(func() { _ = network.Close() })

	result := doHTTPRequest(context.Background(), httpClientRequest{
		id:           1,
		network:      network,
		url:          "https://example.test/",
		method:       http.MethodGet,
		tlsConfigErr: "policy failed",
	})
	if result.err != "TLS config: policy failed" {
		t.Fatalf("HTTPS TLS config error = %q, want policy failed", result.err)
	}
}

func TestHTTPClientMenuRefreshesTLSConfigListOnInbox(t *testing.T) {
	storage := newNodeTestTLSConfigStorage(map[string]string{
		DefaultTlsConfigName: "default.test",
	})
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	if err := w.AddNodeClass(httpClientClass{engine: &engine{tlsConfigs: storage}}); err != nil {
		t.Fatalf("AddNodeClass HTTPClient: %v", err)
	}

	state := formular.NewMenuSnapshotState()
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind == pasta.NotificationNodeMenu {
			state.Apply(notification.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("Subscribe returned 0")
	}
	defer w.UnsubscribeNotifications(sub)

	clientID, err := w.AddNodeByClass(NodeTypeHTTPClient, "client")
	if err != nil {
		t.Fatalf("AddNodeByClass HTTPClient: %v", err)
	}
	w.SubscribeNodeMenu(clientID, 1)
	assertConfigFieldAllowedValues(t, state, clientID, "request", nodeTLSConfigFieldID, []any{DefaultTlsConfigName})

	storage.set("custom", "custom.test")
	w.SendInbox(pasta.InboxMessage{
		ReceiverNode: clientID,
		Payload:      TlsConfigChanged{NamePrev: "custom", NameNew: "custom"},
	})
	assertConfigFieldAllowedValues(t, state, clientID, "request", nodeTLSConfigFieldID, []any{DefaultTlsConfigName, "custom"})
}

func TestHTTPClientRealtimeTLSConfigSelectorSavesSelection(t *testing.T) {
	storage := newNodeTestTLSConfigStorage(map[string]string{
		DefaultTlsConfigName: "default.test",
		"custom":             "custom.test",
	})
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	if err := w.AddNodeClass(httpClientClass{engine: &engine{tlsConfigs: storage}}); err != nil {
		t.Fatalf("AddNodeClass HTTPClient: %v", err)
	}
	clientID, err := w.AddNodeByClass(NodeTypeHTTPClient, "client")
	if err != nil {
		t.Fatalf("AddNodeByClass HTTPClient: %v", err)
	}

	w.SendNodeFormularMsg(clientID, formular.FieldUpdateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldUpdate, MenuID: pasta.NodeMenuID(clientID)},
		Field:       formular.FieldRef{BlockID: "request", FieldID: nodeTLSConfigFieldID},
		Value:       std.String(" custom "),
	})

	out := configer.NewMemory(nil)
	if err := w.SaveConfig(out); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got := configer.String(out.View(configer.Path{"client"}), configer.Path{nodeTLSConfigFieldID}, "")
	if got != "custom" {
		t.Fatalf("saved TLS config = %q, want custom", got)
	}
	storage.assertGets(t, []nodeTestTLSGet{
		{name: DefaultTlsConfigName, node: "client"},
		{name: "custom", node: "client"},
	})
}
