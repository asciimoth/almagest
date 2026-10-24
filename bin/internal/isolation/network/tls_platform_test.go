package network

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

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
