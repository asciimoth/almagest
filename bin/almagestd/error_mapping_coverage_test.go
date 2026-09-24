package main

import (
	"errors"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/lib/storage"
)

func TestStorageRequestErrorMappings(t *testing.T) {
	tests := []struct {
		name string
		call func(string, error) (api.Message, error)
		err  error
		code api.ErrorCode
	}{
		{"dns closed", dnsRequestError, errDNSClosed, api.ErrorUnavailable},
		{"dns name", dnsRequestError, errDNSInvalidName, api.ErrorBadRequest},
		{"dns record", dnsRequestError, errDNSInvalidRecord, api.ErrorBadRequest},
		{"dns value", dnsRequestError, errDNSInvalidValue, api.ErrorInternal},
		{"dns other", dnsRequestError, errors.New("other"), api.ErrorInternal},
		{"kv closed", kvRequestError, storage.ErrKVClosed, api.ErrorUnavailable},
		{"kv name", kvRequestError, storage.ErrKVInvalidName, api.ErrorBadRequest},
		{"kv value", kvRequestError, storage.ErrKVInvalidValue, api.ErrorBadRequest},
		{"kv kind", kvRequestError, storage.ErrKVKindConflict, api.ErrorConflict},
		{"kv root", kvRequestError, storage.ErrKVRoot, api.ErrorConflict},
		{"kv other", kvRequestError, errors.New("other"), api.ErrorInternal},
		{"secret closed", secretRequestError, storage.ErrSecretClosed, api.ErrorUnavailable},
		{"secret name", secretRequestError, storage.ErrSecretInvalidName, api.ErrorBadRequest},
		{"secret node", secretRequestError, errSecretUnknownNode, api.ErrorNotFound},
		{"secret missing", secretRequestError, errSecretNotFound, api.ErrorNotFound},
		{"secret readonly", secretRequestError, storage.ErrSecretReadOnly, api.ErrorConflict},
		{"secret conflict", secretRequestError, storage.ErrSecretConflict, api.ErrorConflict},
		{"secret engine", secretRequestError, errSecretEngineUnavailable, api.ErrorUnavailable},
		{"secret other", secretRequestError, errors.New("other"), api.ErrorInternal},
		{"tls value", tlsRequestError, storage.ErrTlsConfigInvalidValue, api.ErrorBadRequest},
		{"tls cert missing", tlsRequestError, errTLSCertNotFound, api.ErrorNotFound},
		{"tls policy missing", tlsRequestError, errTLSPolicyNotFound, api.ErrorNotFound},
		{"tls readonly", tlsRequestError, storage.ErrTlsConfigReadOnly, api.ErrorConflict},
		{"tls conflict", tlsRequestError, storage.ErrTlsConfigConflict, api.ErrorConflict},
		{"tls other", tlsRequestError, errors.New("other"), api.ErrorInternal},
		{"snapshot missing", snapshotRequestError, errSnapshotNotFound, api.ErrorNotFound},
		{"snapshot invalid", snapshotRequestError, errSnapshotInvalid, api.ErrorInvalidPayload},
		{"snapshot large", snapshotRequestError, errSnapshotTooLarge, api.ErrorTooLarge},
		{"snapshot other", snapshotRequestError, errors.New("other"), api.ErrorInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message, err := test.call("id", test.err)
			if err != nil || message.Error == nil || message.Error.Code != test.code {
				t.Fatalf("response = %#v, err=%v", message, err)
			}
		})
	}
}

func TestDaemonMiscellaneousHelpers(t *testing.T) {
	if _, err := newDaemonRandomSeeds(); err != nil {
		t.Fatal(err)
	}
	logProjectVersionStatus(api.ProjectVersionStatus{Version: "test", HoverMessage: " first\n\n second "})
	logProjectVersionStatus(api.ProjectVersionStatus{Version: "test", Highlight: api.ProjectVersionHighlightWarn})
	if got := (commandNodeClass{}).LongDescription(); got == "" {
		t.Fatal("command node long description is empty")
	}
}
