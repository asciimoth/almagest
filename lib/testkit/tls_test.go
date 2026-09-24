package testkit

import (
	"crypto/tls"
	"errors"
	"reflect"
	"testing"

	"github.com/asciimoth/almagest/lib/storage"
)

func TestTLSConfigStorageLifecycleAndCopies(t *testing.T) {
	input := map[string]string{"first": "first.example"}
	store := NewTLSConfigStorage(input)
	input["first"] = "changed.example"
	store.Set("second", "second.example")

	config, err := store.GetClientConfig("first", "node-a")
	if err != nil || config.ServerName != "first.example" {
		t.Fatalf("GetClientConfig server name = %q, error = %v", config.ServerName, err)
	}
	if _, err := store.GetClientConfig("missing", "node-b"); !errors.Is(err, storage.ErrTlsConfigNotFound) {
		t.Fatalf("missing error = %v", err)
	}
	wantErr := errors.New("configured failure")
	store.SetError("second", wantErr)
	if _, err := store.GetClientConfig("second", "node-c"); !errors.Is(err, wantErr) {
		t.Fatalf("configured error = %v", err)
	}
	if got := store.Gets(); !reflect.DeepEqual(got, []TLSConfigGet{
		{Name: "first", Node: "node-a"},
		{Name: "missing", Node: "node-b"},
		{Name: "second", Node: "node-c"},
	}) {
		t.Fatalf("Gets = %#v", got)
	}
	gets := store.Gets()
	gets[0].Name = "mutated"
	if store.Gets()[0].Name != "first" {
		t.Fatal("Gets returned aliased storage")
	}
	if names := store.ListConfigs(); len(names) != 2 {
		t.Fatalf("ListConfigs = %v", names)
	}

	if _, err := store.GetLocalCA("node"); !errors.Is(err, storage.ErrTlsConfigNotFound) {
		t.Fatalf("empty local CA error = %v", err)
	}
	privateKey := new(int)
	ca := tls.Certificate{
		Certificate:                  [][]byte{{1, 2, 3}},
		PrivateKey:                   privateKey,
		SupportedSignatureAlgorithms: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
		OCSPStaple:                   []byte{4, 5},
		SignedCertificateTimestamps:  [][]byte{{6, 7}},
	}
	store.SetLocalCA(ca)
	ca.Certificate[0][0] = 9
	got, err := store.GetLocalCA("node")
	if err != nil {
		t.Fatal(err)
	}
	if got.Certificate[0][0] != 1 || got.PrivateKey != privateKey {
		t.Fatalf("local CA = %#v", got)
	}
	got.Certificate[0][0] = 8
	again, err := store.GetLocalCA("node")
	if err != nil || again.Certificate[0][0] != 1 {
		t.Fatal("GetLocalCA returned aliased certificate data")
	}
}

func TestCloneTestTLSByteSlicesHandlesEmptyInput(t *testing.T) {
	if got := cloneTestTLSByteSlices(nil); got != nil {
		t.Fatalf("clone nil = %#v", got)
	}
}
