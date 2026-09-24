package main

import (
	"errors"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/lib/storage"
	bberrors "go.etcd.io/bbolt/errors"
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

func TestBboltStorageErrorMappings(t *testing.T) {
	tests := []struct {
		name  string
		call  func(error) error
		input error
		want  error
	}{
		{name: "dns nil", call: mapBboltDNSError},
		{name: "dns closed", call: mapBboltDNSError, input: bberrors.ErrDatabaseNotOpen, want: errDNSClosed},
		{name: "dns tx closed", call: mapBboltDNSError, input: bberrors.ErrTxClosed, want: errDNSClosed},
		{name: "dns bucket", call: mapBboltDNSError, input: bberrors.ErrBucketNameRequired, want: errDNSInvalidName},
		{name: "dns key", call: mapBboltDNSError, input: bberrors.ErrKeyRequired, want: errDNSInvalidName},
		{name: "dns key large", call: mapBboltDNSError, input: bberrors.ErrKeyTooLarge, want: errDNSInvalidName},
		{name: "dns value", call: mapBboltDNSError, input: bberrors.ErrValueTooLarge, want: errDNSInvalidValue},
		{name: "dns incompatible", call: mapBboltDNSError, input: bberrors.ErrIncompatibleValue, want: errDNSInvalidValue},
		{name: "kv nil", call: mapBboltKVError},
		{name: "kv closed", call: mapBboltKVError, input: bberrors.ErrDatabaseNotOpen, want: storage.ErrKVClosed},
		{name: "kv tx closed", call: mapBboltKVError, input: bberrors.ErrTxClosed, want: storage.ErrKVClosed},
		{name: "kv bucket", call: mapBboltKVError, input: bberrors.ErrBucketNameRequired, want: storage.ErrKVInvalidName},
		{name: "kv key", call: mapBboltKVError, input: bberrors.ErrKeyRequired, want: storage.ErrKVInvalidName},
		{name: "kv key large", call: mapBboltKVError, input: bberrors.ErrKeyTooLarge, want: storage.ErrKVInvalidName},
		{name: "kv value", call: mapBboltKVError, input: bberrors.ErrValueTooLarge, want: storage.ErrKVInvalidValue},
		{name: "kv incompatible", call: mapBboltKVError, input: bberrors.ErrIncompatibleValue, want: storage.ErrKVKindConflict},
		{name: "secret nil", call: mapBboltSecretError},
		{name: "secret closed", call: mapBboltSecretError, input: bberrors.ErrDatabaseNotOpen, want: storage.ErrSecretClosed},
		{name: "secret tx closed", call: mapBboltSecretError, input: bberrors.ErrTxClosed, want: storage.ErrSecretClosed},
		{name: "secret bucket", call: mapBboltSecretError, input: bberrors.ErrBucketNameRequired, want: storage.ErrSecretInvalidName},
		{name: "secret key", call: mapBboltSecretError, input: bberrors.ErrKeyRequired, want: storage.ErrSecretInvalidName},
		{name: "secret key large", call: mapBboltSecretError, input: bberrors.ErrKeyTooLarge, want: storage.ErrSecretInvalidName},
		{name: "secret value", call: mapBboltSecretError, input: bberrors.ErrValueTooLarge, want: storage.ErrSecretInvalidValue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.call(test.input)
			if test.want == nil {
				if got != nil {
					t.Fatalf("mapped nil = %v", got)
				}
				return
			}
			if !errors.Is(got, test.want) {
				t.Fatalf("mapped error = %v, want %v", got, test.want)
			}
		})
	}
	other := errors.New("other")
	for _, call := range []func(error) error{mapBboltDNSError, mapBboltKVError, mapBboltSecretError} {
		if got := call(other); got != other {
			t.Fatalf("unknown error = %v, want identity", got)
		}
	}
}
