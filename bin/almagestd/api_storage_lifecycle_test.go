package main

import (
	"path/filepath"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
)

func TestSecretsAndTLSAPISnapshotUnsubscribeAndDelete(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	session := (&AdminAPI{secrets: manager, tls: manager}).newAPISession(nil, nil, nil)
	defer session.close()

	for _, surfaceMethod := range []struct {
		surface api.Surface
		method  string
	}{
		{api.SurfaceSecrets, string(api.MethodSecretsSnapshot)},
		{api.SurfaceTLS, string(api.MethodTLSSnapshot)},
	} {
		response, err := session.handleMessage(mustRequest(t, surfaceMethod.method, surfaceMethod.surface, surfaceMethod.method, nil))
		if err != nil || response.Error != nil {
			t.Fatalf("%s snapshot response = %#v, err=%v", surfaceMethod.surface, response, err)
		}
	}

	secretSubscribe, err := session.handleMessage(mustRequest(t, "secret-sub", api.SurfaceSecrets, string(api.MethodSecretsSubscribe), nil))
	if err != nil || secretSubscribe.Error != nil {
		t.Fatalf("secret subscribe = %#v, %v", secretSubscribe, err)
	}
	secretResult, err := api.DecodePayload[api.SecretsSubscribeResponse](secretSubscribe.Result)
	if err != nil {
		t.Fatal(err)
	}
	response, err := session.handleMessage(mustRequest(t, "secret-unsub", api.SurfaceSecrets, string(api.MethodSecretsUnsubscribe), api.SecretsSubscriptionRequest{SubscriptionID: secretResult.SubscriptionID}))
	if err != nil || response.Error != nil || session.secretsChangeSubscriptionID != 0 {
		t.Fatalf("secret unsubscribe = %#v, err=%v", response, err)
	}

	tlsSubscribe, err := session.handleMessage(mustRequest(t, "tls-sub", api.SurfaceTLS, string(api.MethodTLSSubscribe), nil))
	if err != nil || tlsSubscribe.Error != nil {
		t.Fatalf("TLS subscribe = %#v, %v", tlsSubscribe, err)
	}
	tlsResult, err := api.DecodePayload[api.TLSSubscribeResponse](tlsSubscribe.Result)
	if err != nil {
		t.Fatal(err)
	}
	response, err = session.handleMessage(mustRequest(t, "tls-unsub", api.SurfaceTLS, string(api.MethodTLSUnsubscribe), api.TLSSubscriptionRequest{SubscriptionID: tlsResult.SubscriptionID}))
	if err != nil || response.Error != nil || session.tlsChangeSubscriptionID != 0 {
		t.Fatalf("TLS unsubscribe = %#v, err=%v", response, err)
	}

	response, err = session.handleMessage(mustRequest(t, "builtin", api.SurfaceTLS, string(api.MethodTLSBuiltinCA), nil))
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorNotFound {
		t.Fatalf("missing built-in CA response = %#v, err=%v", response, err)
	}
	response, err = session.handleMessage(mustRequest(t, "builtin-crl", api.SurfaceTLS, string(api.MethodTLSBuiltinCRL), nil))
	if err != nil || response.Error == nil || response.Error.Code != api.ErrorNotFound {
		t.Fatalf("missing built-in CRL response = %#v, err=%v", response, err)
	}
	pemText := testTLSCAPEM(t, "API lifecycle")
	response, err = session.handleMessage(mustRequest(t, "set-cert", api.SurfaceTLS, string(api.MethodTLSSetCert), api.TLSSetCertRequest{Name: "api-ca", PEM: pemText}))
	if err != nil || response.Error != nil {
		t.Fatalf("set certificate response = %#v, err=%v", response, err)
	}
	response, err = session.handleMessage(mustRequest(t, "delete-cert", api.SurfaceTLS, string(api.MethodTLSDeleteCert), api.TLSNameRequest{Name: "api-ca"}))
	if err != nil || response.Error != nil {
		t.Fatalf("delete certificate response = %#v, err=%v", response, err)
	}
}
