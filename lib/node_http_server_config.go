package almagest

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/pasta/pasta"
)

func (n *httpServerNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		"host":     n.host,
		"port":     n.port,
		"response": n.response,
	})
}

func (n *httpServerNode) setHost(value string) {
	if n.setHostNoRefresh(value) {
		_ = n.updateLabel()
		n.sendListenBlock()
		n.restartWorker()
		return
	}
	n.sendListenBlock()
}

func (n *httpServerNode) setHostNoRefresh(value string) bool {
	if value == "" {
		value = DefaultHTTPServerHost
	}
	if n.host == value {
		return false
	}
	n.host = value
	return true
}

func (n *httpServerNode) setPortString(value string) {
	port := parsePort(value, DefaultHTTPServerPort)
	if n.setPortNoRefresh(port) {
		_ = n.updateLabel()
		n.sendListenBlock()
		n.restartWorker()
		return
	}

	n.sendListenBlock()
}

func (n *httpServerNode) setPortNoRefresh(port int) bool {
	if port <= 0 {
		port = DefaultHTTPServerPort
	}
	if n.port == port {
		return false
	}
	n.port = port
	return true
}
