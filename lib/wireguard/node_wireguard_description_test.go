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
		"standard SOCKS5 UDP ASSOCIATE",
		"does not let the client request the relay listen port",
		"?gost",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("WireGuard long description missing %q: %s", want, desc)
		}
	}
}
