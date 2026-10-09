package admingate

import (
	"net"
	"testing"
)

func TestListenerConfigRejectsAuthorityExpansion(t *testing.T) {
	valid := EndpointConfig{ID: 1, Kind: EndpointWeb, Address: "127.0.0.1:8080"}
	for name, mutate := range map[string]func(*EndpointConfig){
		"zero ID":       func(c *EndpointConfig) { c.ID = 0 },
		"empty address": func(c *EndpointConfig) { c.Address = "" },
		"wildcard":      func(c *EndpointConfig) { c.Address = "0.0.0.0:8080" },
		"hostname":      func(c *EndpointConfig) { c.Address = "localhost:8080" },
		"bad port":      func(c *EndpointConfig) { c.Address = "127.0.0.1:no" },
		"long username": func(c *EndpointConfig) { c.Filters.Usernames = []string{string(make([]byte, 257))} },
	} {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			if err := validateListenerConfig(config); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	if err := validateListenerConfig(valid); err != nil {
		t.Fatal(err)
	}
}

func TestListenAllRollsBackEarlierEndpoints(t *testing.T) {
	endpoints, err := ListenAll([]EndpointConfig{
		{ID: 1, Kind: EndpointWeb, Address: "127.0.0.1:0"},
		{ID: 1, Kind: EndpointWeb, Address: "127.0.0.1:0"},
	})
	if err == nil || endpoints != nil {
		t.Fatalf("ListenAll = %#v, %v", endpoints, err)
	}
	// A new listener can bind, which also gives the race detector a real close
	// path to observe. The first endpoint used an ephemeral port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = listener.Close()
}

func FuzzListenerConfig(f *testing.F) {
	f.Add(uint16(1), "web", "127.0.0.1:8080", "alice")
	f.Add(uint16(0), "pipe", `\\.\pipe\admin`, "")
	f.Fuzz(func(t *testing.T, id uint16, kind, address, username string) {
		config := EndpointConfig{ID: id, Kind: EndpointKind(kind), Address: address}
		if username != "" {
			config.Filters.Usernames = []string{username}
		}
		err := validateListenerConfig(config)
		if err == nil && (id == 0 || address == "" || len(username) > 256) {
			t.Fatal("invalid listener config accepted")
		}
	})
}
