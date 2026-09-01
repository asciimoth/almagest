package wireguardnodes

import (
	"strings"
	"testing"
)

func TestWireGuardClassLongDescriptionDocumentsPlainSocksLimit(t *testing.T) {
	desc := WireGuardClass{}.LongDescription()
	for _, want := range []string{
		"`privateKeySecret` stores only the secret name",
		"`SecretsStorage.NewSecret`",
		"`SecretValueChanged`",
		"left `Peers` port accepts any",
		"number of `pasta/object` links",
		"Peers configured in the node menu",
		"always win conflicts with object peers",
		"left `WGCtrl` port provides detached",
		"WGConfig peers coexist with menu and object peers",
		"switching a `pasta/Select` path releases only",
		"standard SOCKS5 UDP ASSOCIATE",
		"does not let the client request the relay listen port",
		"?gost",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("WireGuard long description missing %q: %s", want, desc)
		}
	}
}

func TestWGConfigClassLongDescriptionDocumentsOwnershipAndDropBehavior(t *testing.T) {
	desc := WGConfigClass{}.LongDescription()
	for _, want := range []string{
		"right `WireGuard` port",
		"left `WGCtrl` port",
		"Several WGConfig nodes",
		"`pasta/Select`",
		"class and workspace node ID",
		"Outbound traffic for those peers is then dropped",
		"`presharedKeySecret`",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("WGConfig long description missing %q: %s", want, desc)
		}
	}
}
