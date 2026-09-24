package httpnodes

import "testing"

func TestHTTPClientScalarSetters(t *testing.T) {
	node := &httpClientNode{url: DefaultHTTPClientURL, method: DefaultHTTPClientMethod}
	if node.setURLNoRefresh(" ") {
		t.Fatal("empty URL changed the default")
	}
	if !node.setURLNoRefresh(" https://example.test/path ") || node.url != "https://example.test/path" {
		t.Fatalf("URL = %q", node.url)
	}
	if !node.setMethodNoRefresh(" post ") || node.method != "POST" {
		t.Fatalf("method = %q", node.method)
	}
	if node.setMethodNoRefresh("POST") {
		t.Fatal("unchanged method reported a change")
	}
	if !node.setBodyNoRefresh("body") || node.body != "body" || node.setBodyNoRefresh("body") {
		t.Fatalf("body = %q", node.body)
	}
	if got := node.currentStatusText(); got != httpClientStatusReady {
		t.Fatalf("empty status = %q", got)
	}
	node.statusText = "done"
	if got := node.currentStatusText(); got != "done" {
		t.Fatalf("status = %q", got)
	}
}

func TestHTTPClientRefreshingSettersWithoutWorkspace(t *testing.T) {
	node := &httpClientNode{url: DefaultHTTPClientURL, method: DefaultHTTPClientMethod}
	node.setURL(DefaultHTTPClientURL)
	node.setURL("https://example.test")
	node.setMethod(DefaultHTTPClientMethod)
	node.setMethod("post")
	node.setBody("")
	node.setBody("body")
	if node.url != "https://example.test" || node.method != "POST" || node.body != "body" {
		t.Fatalf("client values = %q, %q, %q", node.url, node.method, node.body)
	}
}

func TestHTTPServerScalarSetters(t *testing.T) {
	node := &httpServerNode{host: DefaultHTTPServerHost, port: DefaultHTTPServerPort}
	if node.setHostNoRefresh("") {
		t.Fatal("empty host changed the default")
	}
	if !node.setHostNoRefresh("localhost") || node.host != "localhost" || node.setHostNoRefresh("localhost") {
		t.Fatalf("host = %q", node.host)
	}
	if node.setPortNoRefresh(0) {
		t.Fatal("invalid port changed the default")
	}
	if !node.setPortNoRefresh(9090) || node.port != 9090 || node.setPortNoRefresh(9090) {
		t.Fatalf("port = %d", node.port)
	}
	if parsePort("1234", 80) != 1234 || parsePort("invalid", 80) != 80 {
		t.Fatal("parsePort returned an unexpected value")
	}
}

func TestHTTPServerUnchangedRefreshingSettersWithoutWorkspace(t *testing.T) {
	node := &httpServerNode{host: DefaultHTTPServerHost, port: DefaultHTTPServerPort}
	node.setHost(DefaultHTTPServerHost)
	node.setPortString("invalid")
	if node.host != DefaultHTTPServerHost || node.port != DefaultHTTPServerPort {
		t.Fatalf("server values = %q, %d", node.host, node.port)
	}
}
