package dnsnodes

import (
	"context"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
)

func dnsTestQuery(name string, typ uint16) *dns.Message {
	return &dns.Message{
		ID:               dns.NextID(),
		Opcode:           dns.OpcodeQuery,
		RecursionDesired: true,
		Questions: []dns.Question{{
			Name:  name,
			Type:  typ,
			Class: dns.ClassIN,
		}},
	}
}

type testRuntime struct {
	sys sysnet.System
}

func (r testRuntime) Context() context.Context { return context.Background() }
func (r testRuntime) Pool() bufpool.Pool       { return nil }
func (r testRuntime) System() sysnet.System    { return r.sys }
func (r testRuntime) KVManager() storage.KVManager {
	return nil
}
func (r testRuntime) DnsCacheManager() storage.DnsCacheManager {
	return nil
}
func (r testRuntime) SecretsStorage() storage.SecretsStorage {
	return nil
}
func (r testRuntime) TlsConfigStorage() storage.TlsConfigStorage {
	return nil
}
func (r testRuntime) WorkerSpawner(uint64) nodekit.WorkerSpawner {
	return nil
}
func (r testRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}
