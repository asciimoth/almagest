package wireguardnodes

import (
	"errors"
	"strings"
	"testing"
)

type wireGuardSecretsFunc struct {
	get func(string, string) (Secret, error)
	new func(Secret, string) (string, error)
}

func (s wireGuardSecretsFunc) GetSecret(key, node string) (Secret, error) {
	if s.get == nil {
		return Secret{}, errors.New("get failed")
	}
	return s.get(key, node)
}

func (s wireGuardSecretsFunc) NewSecret(secret Secret, node string) (string, error) {
	if s.new == nil {
		return "", errors.New("new failed")
	}
	return s.new(secret, node)
}

func TestResolveWireGuardPresharedKeySecretBranches(t *testing.T) {
	valid, _ := wireGuardTestKeyPair(t, 91)
	if key, name, err := resolveWireGuardPresharedKeySecret(nil, "node", "", "", ""); err != nil || key != "" || name != "" {
		t.Fatalf("empty secret = %q, %q, %v", key, name, err)
	}
	if _, _, err := resolveWireGuardPresharedKeySecret(nil, "node", "", valid, "peer"); !errors.Is(err, errWireGuardSecretsUnavailable) {
		t.Fatalf("missing storage error = %v", err)
	}
	storage := newWireGuardTestSecretsStorage()
	if _, _, err := resolveWireGuardPresharedKeySecret(storage, "", "", valid, "peer"); !errors.Is(err, ErrSecretInvalidName) {
		t.Fatalf("missing node error = %v", err)
	}
	name, err := storage.NewSecret(Secret{Value: valid}, "node")
	if err != nil {
		t.Fatal(err)
	}
	if key, gotName, err := resolveWireGuardPresharedKeySecret(storage, "node", name, "", "peer"); err != nil || key != valid || gotName != name {
		t.Fatalf("stored secret = %q, %q, %v", key, gotName, err)
	}
	storage.SetSecret("invalid", Secret{Value: "bad"}, "node")
	if _, _, err := resolveWireGuardPresharedKeySecret(storage, "node", "invalid", "", "peer"); err == nil {
		t.Fatal("invalid stored preshared key was accepted")
	}
	if _, _, err := resolveWireGuardPresharedKeySecret(storage, "node", "missing", "", "peer"); err == nil {
		t.Fatal("missing stored preshared key was accepted")
	}
	if _, _, err := resolveWireGuardPresharedKeySecret(storage, "node", "", "bad", "peer"); err == nil {
		t.Fatal("invalid legacy preshared key was accepted")
	}
	want := errors.New("create failed")
	failing := wireGuardSecretsFunc{new: func(Secret, string) (string, error) { return "", want }}
	if _, _, err := resolveWireGuardPresharedKeySecret(failing, "node", "", valid, "peer"); !errors.Is(err, want) {
		t.Fatalf("create error = %v, want %v", err, want)
	}
	empty := wireGuardSecretsFunc{new: func(Secret, string) (string, error) { return " ", nil }}
	if _, _, err := resolveWireGuardPresharedKeySecret(empty, "node", "", valid, ""); !errors.Is(err, ErrSecretInvalidName) {
		t.Fatalf("empty name error = %v", err)
	}
}

func TestResolveWireGuardPrivateKeySecretFailureBranches(t *testing.T) {
	valid, _ := wireGuardTestKeyPair(t, 92)
	storage := newWireGuardTestSecretsStorage()
	if _, _, err := resolveWireGuardPrivateKeySecret(storage, "", "", valid); !errors.Is(err, ErrSecretInvalidName) {
		t.Fatalf("missing node error = %v", err)
	}
	if _, _, err := resolveWireGuardPrivateKeySecret(storage, "node", "", "bad"); err == nil {
		t.Fatal("invalid legacy private key was accepted")
	}
	want := errors.New("create failed")
	failing := wireGuardSecretsFunc{new: func(Secret, string) (string, error) { return "", want }}
	if _, _, err := resolveWireGuardPrivateKeySecret(failing, "node", "", valid); !errors.Is(err, want) {
		t.Fatalf("create error = %v, want %v", err, want)
	}
	empty := wireGuardSecretsFunc{new: func(Secret, string) (string, error) { return " ", nil }}
	if _, _, err := resolveWireGuardPrivateKeySecret(empty, "node", "", valid); !errors.Is(err, ErrSecretInvalidName) {
		t.Fatalf("empty name error = %v", err)
	}
}

func TestWireGuardSecretSelectionHelpers(t *testing.T) {
	config := wireGuardConfig{
		privateKeySecret: "private",
		peers:            []wireGuardPeerConfig{{presharedKeySecret: "peer-secret"}},
	}
	tests := []struct {
		prev string
		next string
		want bool
	}{
		{prev: "", next: "", want: false},
		{prev: "private", next: "private", want: true},
		{prev: "old", next: "private", want: true},
		{prev: "peer-secret", next: "peer-secret", want: true},
		{prev: "other", next: "other", want: false},
	}
	for _, test := range tests {
		if got := wireGuardSecretChangeRelevant(config, test.prev, test.next); got != test.want {
			t.Fatalf("relevant(%q, %q) = %t, want %t", test.prev, test.next, got, test.want)
		}
	}
	if got := shortWireGuardKey(strings.Repeat("x", 20)); got != strings.Repeat("x", 12) {
		t.Fatalf("short key = %q", got)
	}
	if got := shortWireGuardKey("short"); got != "short" {
		t.Fatalf("short key changed = %q", got)
	}
	if got := peerLabel(wireGuardPeerConfig{id: "id", publicKey: "key"}); got != "id" {
		t.Fatalf("ID peer label = %q", got)
	}
	if got := peerLabel(wireGuardPeerConfig{publicKey: "key"}); got != "key" {
		t.Fatalf("key peer label = %q", got)
	}
	if got := peerLabel(wireGuardPeerConfig{}); got != "peer" {
		t.Fatalf("default peer label = %q", got)
	}
}
