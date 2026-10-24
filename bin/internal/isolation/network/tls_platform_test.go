package network

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/tlsplatform"
)

func TestTLSPlatformRefreshIsBrokerOwnedAndChunked(t *testing.T) {
	b, _ := testBroker(t)
	calls := 0
	provider := tlsplatform.ProviderFunc(func(_ context.Context, request tlsplatform.Request) (tlsplatform.Snapshot, error) {
		calls++
		if request != tlsplatform.DefaultRequest() {
			t.Fatalf("request = %#v", request)
		}
		return tlsplatform.Snapshot{Authorities: []tlsplatform.AuthorityBlob{{SourceID: "large", DER: make([]byte, 80<<10)}}}, nil
	})
	if err := b.SetTLSPlatformProvider(provider); err != nil {
		t.Fatal(err)
	}
	request := tlsplatform.DefaultRequest()
	query := protocol.TLSPlatformRefreshRequest{Meta: meta(b, ""), Source: uint8(request.Source), MaxBytes: request.MaxBytes, MaxItems: request.MaxItems}
	var data []byte
	var transfer uint64
	for {
		query.Transfer, query.Offset = transfer, uint32(len(data))
		result, err := b.TLSPlatformRefresh(context.Background(), query)
		if err != nil {
			t.Fatal(err)
		}
		if transfer != 0 && transfer != result.Transfer {
			t.Fatal("transfer ID changed")
		}
		transfer = result.Transfer
		data = append(data, result.Data...)
		if result.Final {
			break
		}
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	if _, err := tlsplatform.Unmarshal(data, request); err != nil {
		t.Fatal(err)
	}
}

func TestTLSPlatformRefreshFailsClosed(t *testing.T) {
	b, _ := testBroker(t)
	request := tlsplatform.DefaultRequest()
	query := protocol.TLSPlatformRefreshRequest{Meta: meta(b, ""), Source: uint8(request.Source), MaxBytes: request.MaxBytes, MaxItems: request.MaxItems}
	if _, err := b.TLSPlatformRefresh(context.Background(), query); !errors.Is(err, protocol.ErrCapabilityUnavailable) {
		t.Fatalf("missing provider error = %v", err)
	}
	provider := tlsplatform.ProviderFunc(func(context.Context, tlsplatform.Request) (tlsplatform.Snapshot, error) {
		return tlsplatform.Snapshot{}, errors.New("store unavailable")
	})
	if err := b.SetTLSPlatformProvider(provider); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TLSPlatformRefresh(context.Background(), query); err == nil || err.Error() != "store unavailable" {
		t.Fatalf("provider error = %v", err)
	}
	query.Source = 99
	if _, err := b.TLSPlatformRefresh(context.Background(), query); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("source error = %v", err)
	}
}

func TestTLSPlatformDispatchRejectsHostSelectors(t *testing.T) {
	b, _ := testBroker(t)
	request := tlsplatform.DefaultRequest()
	body, err := json.Marshal(struct {
		protocol.TLSPlatformRefreshRequest
		Path string
	}{TLSPlatformRefreshRequest: protocol.TLSPlatformRefreshRequest{Meta: meta(b, ""), Source: uint8(request.Source), MaxBytes: request.MaxBytes, MaxItems: request.MaxItems}, Path: "/etc/ssl/private"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Handle(context.Background(), protocol.OperationTLSPlatformRefresh, body); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("path-bearing request error = %v", err)
	}
}

func TestTLSRevocationCheckIsClosedAndBrokerOwned(t *testing.T) {
	b, _ := testBroker(t)
	der := testTLSCertificateDER(t)
	request := tlsplatform.RevocationRequest{Chains: [][][]byte{{der, der}}}
	query := protocol.TLSRevocationCheckRequest{Meta: meta(b, ""), Chains: request.Chains}
	if _, err := b.TLSRevocationCheck(context.Background(), query); !errors.Is(err, protocol.ErrCapabilityUnavailable) {
		t.Fatalf("disabled checker error = %v", err)
	}
	checker := tlsplatform.RevocationCheckerFunc(func(_ context.Context, got tlsplatform.RevocationRequest) (tlsplatform.RevocationResult, error) {
		if len(got.Chains) != 1 || len(got.Chains[0]) != 2 {
			t.Fatalf("request = %#v", got)
		}
		return tlsplatform.RevocationResult{States: []tlsplatform.RevocationState{tlsplatform.RevocationRevoked}}, nil
	})
	if err := b.SetTLSRevocationChecker(checker); err != nil {
		t.Fatal(err)
	}
	result, err := b.TLSRevocationCheck(context.Background(), query)
	if err != nil || len(result.States) != 1 || result.States[0] != string(tlsplatform.RevocationRevoked) {
		t.Fatalf("result = %#v, %v", result, err)
	}
	body, err := json.Marshal(struct {
		protocol.TLSRevocationCheckRequest
		URL string
	}{TLSRevocationCheckRequest: query, URL: "http://attacker.invalid/list.crl"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Handle(context.Background(), protocol.OperationTLSRevocationCheck, body); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("request with URL error = %v", err)
	}
}

func testTLSCertificateDER(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
