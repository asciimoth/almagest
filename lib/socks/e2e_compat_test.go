package socksnodes_test

import (
	"context"
	"testing"

	almagest "github.com/asciimoth/almagest/lib"
	httpnodes "github.com/asciimoth/almagest/lib/http"
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/logging"
	socksnodes "github.com/asciimoth/almagest/lib/socks"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

const (
	E2EWorkspace = e2etest.E2EWorkspace
	TypeNetwork  = links.TypeNetwork

	NodeTypeHTTPServer  = httpnodes.NodeTypeHTTPServer
	NodeTypeLocalProxy  = socksnodes.NodeTypeLocalProxy
	NodeTypeSocksClient = socksnodes.NodeTypeSocksClient
	NodeTypeSocksServer = socksnodes.NodeTypeSocksServer
)

type (
	NodeE2E        = e2etest.NodeE2E
	NetworkPayload = links.NetworkPayload
)

func BuildEngine(ctx context.Context, cfg configer.Config, logf logging.LogFactory, sys sysnet.System, kv storage.KVManager, dns storage.DnsCacheManager, secrets storage.SecretsStorage, tlsConfigs storage.TlsConfigStorage, extra []pasta.NodeClass, pool bufpool.Pool, debug bool) (almagest.Engine, error) {
	return almagest.BuildEngine(ctx, cfg, logf, sys, kv, dns, secrets, tlsConfigs, extra, pool, debug)
}

func NewDebugLogFactory(buffer int) *logging.DebugLogFactory {
	return logging.NewDebugLogFactory(buffer)
}

func NewNodeE2E(t *testing.T) *NodeE2E {
	return e2etest.NewNodeE2E(t)
}

func NewNodeE2EFromEngine(t *testing.T, engine almagest.Engine, logf *logging.DebugLogFactory, sys *sysnetdebug.System, secrets *e2etest.SecretsStorage) *NodeE2E {
	return e2etest.NewNodeE2EFromEngine(t, engine, logf, sys, secrets)
}

func newE2EDebugSystem() *sysnetdebug.System {
	return e2etest.NewDebugSystem()
}

func newE2ETestDebugPool(t *testing.T) *e2etest.DebugPool {
	return e2etest.NewTestDebugPool(t)
}

func httpTestPort() int {
	return e2etest.TestPort()
}

func FormMessageBase(messageType string, node uint64) formular.MessageBase {
	return testkit.FormMessageBase(messageType, node)
}

func Eventually(t *testing.T, condition func() bool, format string, args ...any) {
	testkit.Eventually(t, condition, format, args...)
}

func NetworkPort(direction, name string) pasta.Port {
	return links.NetworkPort(direction, name)
}

func BindNetworkResource(w *pasta.Workspace, node, link uint64, n links.NetworkCloser) {
	links.BindNetworkResource(w, node, link, n)
}
