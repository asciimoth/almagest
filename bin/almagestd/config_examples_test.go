package main

import (
	"os"
	"testing"

	"github.com/asciimoth/configer/configer"
)

func TestWireGuardExamplesUseProtectedSecrets(t *testing.T) {
	protectedData, err := os.ReadFile("../../examples/protected_alm.hujson")
	if err != nil {
		t.Fatalf("read examples protected config: %v", err)
	}
	cases := []struct {
		name    string
		secrets map[string]string
	}{
		{
			name: "socks2wireguard.hujson",
			secrets: map[string]string{
				"WireGuard Client": "wireguard-client-private-key",
				"WireGuard Server": "wireguard-server-private-key",
			},
		},
		{
			name: "wireguard_over_socks.hujson",
			secrets: map[string]string{
				"WireGuard Client": "wireguard-client-private-key",
				"WireGuard Server": "wireguard-server-private-key",
			},
		},
		{
			name: "nested_wireguard.hujson",
			secrets: map[string]string{
				"Inner WireGuard Client": "inner-wireguard-client-private-key",
				"Inner WireGuard Server": "inner-wireguard-server-private-key",
				"WireGuard Client":       "wireguard-client-private-key",
				"WireGuard Server":       "wireguard-server-private-key",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mainData, err := os.ReadFile("../../examples/" + tc.name)
			if err != nil {
				t.Fatalf("read example config: %v", err)
			}
			cfg, err := ParseConfig(mainData, protectedData)
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			protectedSecrets, err := cfg.Protected.ImmutableSecrets()
			if err != nil {
				t.Fatalf("ImmutableSecrets: %v", err)
			}
			for nodeName, secretName := range tc.secrets {
				got := configer.String(cfg.Main, configer.Path{"nodes", nodeName, "privateKeySecret"}, "")
				if got != secretName {
					t.Fatalf("%s privateKeySecret = %q, want %q", nodeName, got, secretName)
				}
				if _, err := cfg.Main.Get(configer.Path{"nodes", nodeName, "privateKey"}); err == nil {
					t.Fatalf("%s still stores raw privateKey", nodeName)
				}
				secret, ok := protectedSecrets[secretName]
				if !ok {
					t.Fatalf("protected secret %q missing", secretName)
				}
				if !secretNodeAllowed(secret.Nodes, nodeName) {
					t.Fatalf("protected secret %q does not allow node %q", secretName, nodeName)
				}
			}
		})
	}
}
