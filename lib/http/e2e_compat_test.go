package httpnodes_test

import (
	"context"
	"testing"

	almagest "github.com/asciimoth/almagest/lib"
	httpnodes "github.com/asciimoth/almagest/lib/http"
	"github.com/asciimoth/almagest/lib/logging"
	networknodes "github.com/asciimoth/almagest/lib/network"
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

	NodeTypeHTTPClient  = httpnodes.NodeTypeHTTPClient
	NodeTypeHTTPServer  = httpnodes.NodeTypeHTTPServer
	NodeTypeMockNetwork = networknodes.NodeTypeMockNetwork
	NodeTypeOutNetwork  = networknodes.NodeTypeOutNetwork
	NodeTypeSocksClient = socksnodes.NodeTypeSocksClient
)

type (
	NodeE2E           = e2etest.NodeE2E
	TlsConfigStorage  = storage.TlsConfigStorage
	e2eSecretsStorage = e2etest.SecretsStorage
)

var ErrTlsConfigNotFound = storage.ErrTlsConfigNotFound

func BuildEngine(ctx context.Context, cfg configer.Config, logf logging.LogFactory, sys sysnet.System, kv storage.KVManager, dns storage.DnsCacheManager, secrets storage.SecretsStorage, tlsConfigs storage.TlsConfigStorage, extra []pasta.NodeClass, pool bufpool.Pool, debug bool) (almagest.Engine, error) {
	return almagest.BuildEngine(ctx, cfg, logf, sys, kv, dns, secrets, tlsConfigs, extra, pool, debug)
}

func NewDebugLogFactory(buffer int) *logging.DebugLogFactory {
	return logging.NewDebugLogFactory(buffer)
}

func NewNodeE2E(t *testing.T) *NodeE2E {
	return e2etest.NewNodeE2E(t)
}

func NewNodeE2EFromEngine(t *testing.T, engine almagest.Engine, logf *logging.DebugLogFactory, sys *sysnetdebug.System, secrets *e2eSecretsStorage) *NodeE2E {
	return e2etest.NewNodeE2EFromEngine(t, engine, logf, sys, secrets)
}

func newE2ETestDebugPool(t *testing.T) *e2etest.DebugPool {
	return e2etest.NewTestDebugPool(t)
}

func newE2ESecretsStorage() *e2eSecretsStorage {
	return e2etest.NewSecretsStorage()
}

func httpTestPort() int {
	return e2etest.TestPort()
}

func Eventually(t *testing.T, condition func() bool, format string, args ...any) {
	testkit.Eventually(t, condition, format, args...)
}

func FormMessageBase(messageType string, node uint64) formular.MessageBase {
	return testkit.FormMessageBase(messageType, node)
}
