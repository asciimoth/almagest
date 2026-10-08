package policy_test

import (
	"errors"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func TestDefaultPolicyPreservesSupportedNetworkAuthority(t *testing.T) {
	p := policy.Default(policy.LaunchAuthority{Backend: "linux", CredentialClass: "ordinary-user"}, policy.CommandAuthority{})
	for _, operation := range []protocol.Operation{protocol.OperationCreateTun, protocol.OperationConfigureDNS, protocol.OperationOpenSocket} {
		if !p.AllowsNetwork(operation) {
			t.Fatalf("default policy denies %q", operation)
		}
	}
	if p.AllowsNetwork(protocol.Operation("ioctl")) {
		t.Fatal("default policy permits unknown operation")
	}
}

func TestOptionalPolicyCanOnlyRestrict(t *testing.T) {
	p := policy.Default(policy.LaunchAuthority{Backend: "linux", CredentialClass: "service"}, policy.CommandAuthority{})
	p.NetworkRestrictions = &policy.NetworkRestrictions{DeniedOperations: []protocol.Operation{protocol.OperationConfigureDNS}}
	if p.AllowsNetwork(protocol.OperationConfigureDNS) || !p.AllowsNetwork(protocol.OperationOpenSocket) {
		t.Fatalf("unexpected optional restriction result")
	}
}

func TestCommandAuthoritiesAreSeparate(t *testing.T) {
	p := policy.Default(policy.LaunchAuthority{Backend: "windows", CredentialClass: "service"}, policy.CommandAuthority{
		NodeCommands: []string{"notify"},
		Callbacks:    []string{"tun-up"},
	})
	if err := p.AuthorizeNodeCommand("notify"); err != nil {
		t.Fatal(err)
	}
	if err := p.AuthorizeCallback("tun-up"); err != nil {
		t.Fatal(err)
	}
	if err := p.AuthorizeNodeCommand("tun-up"); !errors.Is(err, policy.ErrNodeCommand) {
		t.Fatalf("callback used as node command: %v", err)
	}
	if err := p.AuthorizeCallback("notify"); !errors.Is(err, policy.ErrCallback) {
		t.Fatalf("node command used as callback: %v", err)
	}
}

func TestPolicyDigestIsOrderIndependent(t *testing.T) {
	launch := policy.LaunchAuthority{Backend: "linux", CredentialClass: "service"}
	a := policy.Default(launch, policy.CommandAuthority{NodeCommands: []string{"b", "a"}})
	b := policy.Default(launch, policy.CommandAuthority{NodeCommands: []string{"a", "b"}})
	digestA, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	digestB, err := b.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digestA != digestB {
		t.Fatal("equivalent normalized policies have different digests")
	}
}
