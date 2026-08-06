package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/configer/configer"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tlspolicy"
	"github.com/asciimoth/pasta/pasta"
)

func TestTLSStorageDefaultPolicyAndImmutableOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	mutablePEM := testTLSCAPEM(t, "mutable")
	protectedPEM := testTLSCAPEM(t, "protected")
	osPEM := testTLSCAPEM(t, "os")

	manager, err := openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setTLSCert("shared", mutablePEM); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.setTLSPolicy("shadow", testTLSPolicy("shared")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	manager, err = openKVManagerWithOptions(path, bboltKVManagerOptions{
		ImmutableTLS: immutableTLSData{
			Certs: map[string]immutableTLSCert{
				"shared": {
					tlsCertificateRecord: tlsCertificateRecord{PEM: protectedPEM},
					source:               api.TLSSourceProtected,
				},
				"os-ca:test": {
					tlsCertificateRecord: tlsCertificateRecord{PEM: osPEM},
					source:               api.TLSSourceOS,
				},
			},
			Policies: map[string]immutableTLSPolicy{
				"shadow": {
					tlsPolicyRecord: tlsPolicyRecord{Spec: testTLSPolicy("shared")},
					source:          api.TLSSourceProtected,
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	snapshot, err := manager.tlsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	shared := findTLSCertEntry(snapshot.Certs, "shared")
	if shared == nil || !shared.ReadOnly || shared.Source != api.TLSSourceProtected || shared.PEM != protectedPEM {
		t.Fatalf("shared cert = %#v, want protected overlay", shared)
	}
	defaultPolicy := findTLSPolicyEntry(snapshot.Policies, tlsDefaultPolicyName)
	if defaultPolicy == nil || !defaultPolicy.ReadOnly || defaultPolicy.Source != api.TLSSourceOS {
		t.Fatalf("default policy = %#v, want immutable OS policy", defaultPolicy)
	}
	if len(defaultPolicy.Spec.TrustAnchors) != 1 || defaultPolicy.Spec.TrustAnchors[0].Cert != "os-ca:test" {
		t.Fatalf("default trust anchors = %#v, want OS cert", defaultPolicy.Spec.TrustAnchors)
	}
	shadow := findTLSPolicyEntry(snapshot.Policies, "shadow")
	if shadow == nil || !shadow.ReadOnly || shadow.Source != api.TLSSourceProtected {
		t.Fatalf("shadow policy = %#v, want protected overlay", shadow)
	}

	if _, err := manager.setTLSCert("shared", testTLSCAPEM(t, "replacement")); !errors.Is(err, almagest.ErrTlsConfigReadOnly) {
		t.Fatalf("set protected cert error = %v, want read-only", err)
	}
	if _, err := manager.setTLSPolicy("shadow", testTLSPolicy("shared")); !errors.Is(err, almagest.ErrTlsConfigReadOnly) {
		t.Fatalf("set protected policy error = %v, want read-only", err)
	}
	if _, err := manager.setTLSCert("os-ca:user", testTLSCAPEM(t, "reserved")); !errors.Is(err, almagest.ErrTlsConfigInvalidName) {
		t.Fatalf("set OS-prefixed cert error = %v, want invalid name", err)
	}

	cfg, err := manager.GetClientConfig("shadow", "client")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RootCAs == nil {
		t.Fatal("GetClientConfig RootCAs = nil, want explicit pool")
	}
}

func TestTLSCertcatAnnotationsAndDefaultPolicy(t *testing.T) {
	warningPEM := testCertcatExamplePEM(t, "warning-interception-root.pem")
	narrowPEM := testCertcatExamplePEM(t, "narrow-scope-ca.pem")
	normalPEM := testTLSCAPEM(t, "ordinary-os-root")

	manager, err := openKVManagerWithOptions(filepath.Join(t.TempDir(), "kv.db"), bboltKVManagerOptions{
		ImmutableTLS: immutableTLSData{
			Certs: map[string]immutableTLSCert{
				"os-ca:warning": {
					tlsCertificateRecord: tlsCertificateRecord{PEM: warningPEM},
					source:               api.TLSSourceOS,
				},
				"os-ca:narrow": {
					tlsCertificateRecord: tlsCertificateRecord{PEM: narrowPEM},
					source:               api.TLSSourceOS,
				},
				"os-ca:normal": {
					tlsCertificateRecord: tlsCertificateRecord{PEM: normalPEM},
					source:               api.TLSSourceOS,
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	snapshot, err := manager.tlsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	warning := findTLSCertEntry(snapshot.Certs, "os-ca:warning")
	if warning == nil || len(warning.CertcatMatches) != 1 {
		t.Fatalf("warning cert = %#v, want one certcat match", warning)
	}
	if got := warning.CertcatMatches[0]; got.Action != "warn" || got.Severity != "warning" || got.Kind != "hazard_certificate" {
		t.Fatalf("warning certcat match = %#v, want warning hazard", got)
	}
	narrow := findTLSCertEntry(snapshot.Certs, "os-ca:narrow")
	if narrow == nil || len(narrow.CertcatMatches) != 1 || narrow.CertcatMatches[0].Action != "narrow_scope" {
		t.Fatalf("narrow cert = %#v, want narrow_scope certcat match", narrow)
	}

	defaultPolicy := findTLSPolicyEntry(snapshot.Policies, tlsDefaultPolicyName)
	if defaultPolicy == nil || !defaultPolicy.Valid {
		t.Fatalf("default policy = %#v, want valid policy", defaultPolicy)
	}
	if anchor := findTLSTrustAnchor(defaultPolicy.Spec.TrustAnchors, "os-ca:warning"); anchor != nil {
		t.Fatalf("default policy included warning cert anchor %#v", anchor)
	}
	normalAnchor := findTLSTrustAnchor(defaultPolicy.Spec.TrustAnchors, "os-ca:normal")
	if normalAnchor == nil || !normalAnchor.Scope.Any {
		t.Fatalf("normal default anchor = %#v, want any scope", normalAnchor)
	}
	narrowAnchor := findTLSTrustAnchor(defaultPolicy.Spec.TrustAnchors, "os-ca:narrow")
	if narrowAnchor == nil || narrowAnchor.Scope.Any {
		t.Fatalf("narrow default anchor = %#v, want restricted scope", narrowAnchor)
	}
	if !tlsScopeHasDNS(narrowAnchor.Scope, "apps.example.internal", true) ||
		!tlsScopeHasDNS(narrowAnchor.Scope, "example.internal", false) {
		t.Fatalf("narrow default anchor scope = %#v, want certcat DNS scope", narrowAnchor.Scope)
	}
}

func TestTLSBuiltinCAGenerationAndPolicyInjection(t *testing.T) {
	storageDir := t.TempDir()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	ca, err := loadOrCreateTLSBuiltinCA(storageDir, api.TLSBuiltinCAOptions{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if ca.options.Issuer != "Almagest" || ca.options.KeyAlgorithm != "ecdsa" || ca.options.Curve != "P256" || ca.options.ValidityDays != 0 {
		t.Fatalf("builtin CA options = %#v, want defaults", ca.options)
	}
	if ca.certificate.Subject.CommonName != "Almagest" || ca.certificate.NotAfter.Year() != 9999 {
		t.Fatalf("builtin CA certificate subject=%q not_after=%s, want Almagest no-expiration fallback",
			ca.certificate.Subject.CommonName, ca.certificate.NotAfter)
	}
	for _, name := range []string{tlsBuiltinCACertFile, tlsBuiltinCAKeyFile, tlsBuiltinCAStateFile} {
		if _, err := os.Stat(filepath.Join(storageDir, tlsBuiltinCADirName, name)); err != nil {
			t.Fatalf("stored builtin CA file %s: %v", name, err)
		}
	}

	manager, err := openKVManagerWithOptions(filepath.Join(storageDir, "kv.db"), bboltKVManagerOptions{
		BuiltinCA: ca,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	snapshot, err := manager.tlsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.BuiltinCA == nil {
		t.Fatal("snapshot BuiltinCA = nil, want generated CA info")
	}
	if snapshot.BuiltinCA.Cert.Name != tlsBuiltinCACertName ||
		!snapshot.BuiltinCA.Cert.ReadOnly ||
		snapshot.BuiltinCA.Cert.Source != api.TLSSourceBuiltin {
		t.Fatalf("builtin CA entry = %#v, want read-only builtin cert", snapshot.BuiltinCA.Cert)
	}
	rawInfo, err := json.Marshal(snapshot.BuiltinCA)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawInfo, []byte("PRIVATE KEY")) {
		t.Fatalf("builtin CA API info leaked private key: %s", rawInfo)
	}

	defaultPolicy := findTLSPolicyEntry(snapshot.Policies, tlsDefaultPolicyName)
	if defaultPolicy == nil {
		t.Fatal("default TLS policy missing")
	}
	builtinAnchor := findTLSTrustAnchor(defaultPolicy.Spec.TrustAnchors, tlsBuiltinCACertName)
	if builtinAnchor == nil || !builtinAnchor.Scope.Any {
		t.Fatalf("default builtin anchor = %#v, want unscoped trust anchor", builtinAnchor)
	}
	if _, err := manager.setTLSCert(tlsBuiltinCACertName, testTLSCAPEM(t, "shadow")); !errors.Is(err, almagest.ErrTlsConfigInvalidName) {
		t.Fatalf("set builtin cert error = %v, want invalid name", err)
	}

	if _, err := manager.setTLSPolicy("custom", api.TLSPolicySpec{}); err != nil {
		t.Fatal(err)
	}
	custom, found, err := manager.tlsPolicyAPIEntry("custom")
	if err != nil || !found {
		t.Fatalf("custom policy found=%v err=%v", found, err)
	}
	builtinAnchor = findTLSTrustAnchor(custom.Spec.TrustAnchors, tlsBuiltinCACertName)
	if builtinAnchor == nil || !builtinAnchor.Scope.Any {
		t.Fatalf("custom builtin anchor = %#v, want unscoped trust anchor", builtinAnchor)
	}
}

func TestTLSBuiltinCARegenerationTriggers(t *testing.T) {
	storageDir := t.TempDir()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	options := api.TLSBuiltinCAOptions{
		Issuer:       "First",
		ValidityDays: 1,
		KeyAlgorithm: "ecdsa",
		Curve:        "P256",
	}
	first, err := loadOrCreateTLSBuiltinCA(storageDir, options, now)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := loadOrCreateTLSBuiltinCA(storageDir, options, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if reused.certPEM != first.certPEM {
		t.Fatal("builtin CA was regenerated even though options matched and certificate was live")
	}

	changedOptions := options
	changedOptions.Issuer = "Second"
	changed, err := loadOrCreateTLSBuiltinCA(storageDir, changedOptions, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if changed.certPEM == first.certPEM || changed.certificate.Subject.CommonName != "Second" {
		t.Fatalf("changed-options CA subject=%q, same cert=%v; want regenerated Second cert",
			changed.certificate.Subject.CommonName, changed.certPEM == first.certPEM)
	}

	expired, err := loadOrCreateTLSBuiltinCA(storageDir, changedOptions, now.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if expired.certPEM == changed.certPEM {
		t.Fatal("expired builtin CA was not regenerated")
	}
}

func TestProtectedConfigTLSBuiltinCA(t *testing.T) {
	notBefore := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	notAfter := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	certPEM, keyPEM := testTLSCAKeyPairPEM(t, "protected builtin", notBefore, notAfter)
	protected := `{
		"TLS": {
			"BuiltinCA": {"PEM": ` + strconv.Quote(certPEM+keyPEM) + `}
		}
	}`
	cfg, err := ParseConfig([]byte(`{}`), []byte(protected))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := cfg.Protected.TLSBuiltinCA()
	if err != nil {
		t.Fatal(err)
	}
	if ca == nil || !ca.immutable {
		t.Fatalf("protected builtin CA = %#v, want immutable CA", ca)
	}
	if ca.options.Issuer != "protected builtin" || ca.options.ValidityDays != 1 || ca.options.KeyAlgorithm != "ecdsa" {
		t.Fatalf("protected builtin CA options = %#v, want derived options", ca.options)
	}
	if !tlsBuiltinCACertExpired(ca.certificate, notAfter.Add(time.Second)) {
		t.Fatal("test protected builtin CA should be expired at test time")
	}

	manager, err := openKVManagerWithOptions(filepath.Join(t.TempDir(), "kv.db"), bboltKVManagerOptions{
		BuiltinCA: ca,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	admin := &AdminAPI{
		config:      cfg,
		fileStorage: t.TempDir(),
		tls:         manager,
	}
	if _, _, err := admin.setTLSBuiltinCAOptions(api.TLSBuiltinCAOptions{Issuer: "mutable"}); !errors.Is(err, almagest.ErrTlsConfigReadOnly) {
		t.Fatalf("set protected builtin CA options error = %v, want read-only", err)
	}

	snapshot, err := manager.tlsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.BuiltinCA == nil || !snapshot.BuiltinCA.Immutable || !snapshot.BuiltinCA.RegenerationDisabled {
		t.Fatalf("protected builtin CA snapshot = %#v, want regeneration disabled", snapshot.BuiltinCA)
	}
}

func TestTLSAdminEventsFixPoliciesAndNotifyFetchedNodes(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	inbox := make(chan almagest.TlsConfigChanged, 8)
	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		almagest.NewDebugLogFactory(16),
		&sysnetdebug.System{},
		nil,
		nil,
		nil,
		manager,
		[]pasta.NodeClass{tlsInboxNodeClass{inbox: inbox}},
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close() //nolint:errcheck
	if _, err := engine.AddNode("main", "test/TLSInbox", "client"); err != nil {
		t.Fatal(err)
	}

	var events []api.Message
	session := (&AdminAPI{tls: manager}).newAPISession(engine, nil, func(msg api.Message) error {
		if msg.Kind == api.MessageEvent && msg.Surface == api.SurfaceTLS {
			events = append(events, msg)
		}
		return nil
	})
	defer session.close()

	subReq := mustRequest(t, "1", api.SurfaceTLS, string(api.MethodTLSSubscribe), nil)
	resp, err := session.handleMessage(subReq)
	if err != nil {
		t.Fatal(err)
	}
	subResp, err := api.DecodePayload[api.TLSSubscribeResponse](resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if subResp.SubscriptionID == 0 || findTLSPolicyEntry(subResp.Snapshot.Policies, tlsDefaultPolicyName) == nil {
		t.Fatalf("subscribe response = %#v, want live subscription with default policy", subResp)
	}

	events = nil
	setCert := mustRequest(t, "2", api.SurfaceTLS, string(api.MethodTLSSetCert), api.TLSSetCertRequest{
		Name: "ca",
		PEM:  testTLSCAPEM(t, "ca-old"),
	})
	if resp, err = session.handleMessage(setCert); err != nil || resp.Error != nil {
		t.Fatalf("set cert response = %#v, err %v", resp, err)
	}
	assertTLSEvent(t, events, subResp.SubscriptionID, api.TLSChangeCert, "ca", false)
	assertNoTLSInbox(t, inbox)

	events = nil
	setPolicy := mustRequest(t, "3", api.SurfaceTLS, string(api.MethodTLSSetPolicy), api.TLSSetPolicyRequest{
		Name: "policy",
		Spec: testTLSPolicy("ca"),
	})
	if resp, err = session.handleMessage(setPolicy); err != nil || resp.Error != nil {
		t.Fatalf("set policy response = %#v, err %v", resp, err)
	}
	assertTLSEvent(t, events, subResp.SubscriptionID, api.TLSChangePolicy, "policy", false)
	assertNoTLSInbox(t, inbox)

	if storage := engine.TlsConfigStorage(); storage == nil {
		t.Fatal("Engine.TlsConfigStorage() = nil, want manager")
	} else if _, err := storage.GetClientConfig("policy", "client"); err != nil {
		t.Fatalf("GetClientConfig policy: %v", err)
	}

	events = nil
	updateCert := mustRequest(t, "4", api.SurfaceTLS, string(api.MethodTLSSetCert), api.TLSSetCertRequest{
		Name: "ca",
		PEM:  testTLSCAPEM(t, "ca-new"),
	})
	if resp, err = session.handleMessage(updateCert); err != nil || resp.Error != nil {
		t.Fatalf("update cert response = %#v, err %v", resp, err)
	}
	assertTLSInbox(t, inbox, "policy", "policy")
	assertTLSEventsContain(t, events, api.TLSChangeCert, "ca", false)
	assertTLSEventsContain(t, events, api.TLSChangePolicy, "policy", false)

	events = nil
	renameCert := mustRequest(t, "5", api.SurfaceTLS, string(api.MethodTLSRenameCert), api.TLSRenameRequest{
		Name:    "ca",
		NewName: "ca2",
	})
	if resp, err = session.handleMessage(renameCert); err != nil || resp.Error != nil {
		t.Fatalf("rename cert response = %#v, err %v", resp, err)
	}
	assertTLSInbox(t, inbox, "policy", "policy")
	assertTLSEventsContain(t, events, api.TLSChangeCert, "ca", true)
	assertTLSEventsContain(t, events, api.TLSChangeCert, "ca2", false)
	assertTLSEventsContain(t, events, api.TLSChangePolicy, "policy", false)
	policy, found, err := manager.tlsPolicyAPIEntry("policy")
	if err != nil || !found {
		t.Fatalf("policy after cert rename found=%v err=%v", found, err)
	}
	if len(policy.Spec.TrustAnchors) != 1 || policy.Spec.TrustAnchors[0].Cert != "ca2" {
		t.Fatalf("policy cert refs after rename = %#v, want ca2", policy.Spec.TrustAnchors)
	}

	events = nil
	renamePolicy := mustRequest(t, "6", api.SurfaceTLS, string(api.MethodTLSRenamePolicy), api.TLSRenameRequest{
		Name:    "policy",
		NewName: "renamed",
	})
	if resp, err = session.handleMessage(renamePolicy); err != nil || resp.Error != nil {
		t.Fatalf("rename policy response = %#v, err %v", resp, err)
	}
	assertTLSInbox(t, inbox, "policy", "renamed")
	assertTLSEventsContain(t, events, api.TLSChangePolicy, "policy", true)
	assertTLSEventsContain(t, events, api.TLSChangePolicy, "renamed", false)

	events = nil
	deletePolicy := mustRequest(t, "7", api.SurfaceTLS, string(api.MethodTLSDeletePolicy), api.TLSNameRequest{Name: "renamed"})
	if resp, err = session.handleMessage(deletePolicy); err != nil || resp.Error != nil {
		t.Fatalf("delete policy response = %#v, err %v", resp, err)
	}
	assertTLSInbox(t, inbox, "renamed", "renamed")
	assertTLSEvent(t, events, subResp.SubscriptionID, api.TLSChangePolicy, "renamed", true)
}

func TestTLSBuiltinCAAPIRegeneratesAndNotifiesPolicies(t *testing.T) {
	storageDir := t.TempDir()
	ca, err := loadOrCreateTLSBuiltinCA(storageDir, api.TLSBuiltinCAOptions{}, time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := openKVManagerWithOptions(filepath.Join(storageDir, "kv.db"), bboltKVManagerOptions{
		BuiltinCA: ca,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	if _, err := manager.setTLSPolicy("policy", api.TLSPolicySpec{}); err != nil {
		t.Fatal(err)
	}

	inbox := make(chan almagest.TlsConfigChanged, 8)
	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		almagest.NewDebugLogFactory(16),
		&sysnetdebug.System{},
		nil,
		nil,
		nil,
		manager,
		[]pasta.NodeClass{tlsInboxNodeClass{inbox: inbox}},
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close() //nolint:errcheck
	if _, err := engine.AddNode("main", "test/TLSInbox", "client"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.GetClientConfig("policy", "client"); err != nil {
		t.Fatal(err)
	}

	cfg, err := ParseConfig([]byte(`{}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var events []api.Message
	session := (&AdminAPI{tls: manager, config: cfg, fileStorage: storageDir}).newAPISession(engine, nil, func(msg api.Message) error {
		if msg.Kind == api.MessageEvent && msg.Surface == api.SurfaceTLS {
			events = append(events, msg)
		}
		return nil
	})
	defer session.close()

	subReq := mustRequest(t, "1", api.SurfaceTLS, string(api.MethodTLSSubscribe), nil)
	resp, err := session.handleMessage(subReq)
	if err != nil || resp.Error != nil {
		t.Fatalf("subscribe response = %#v, err %v", resp, err)
	}
	subResp, err := api.DecodePayload[api.TLSSubscribeResponse](resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	events = nil
	setReq := mustRequest(t, "2", api.SurfaceTLS, string(api.MethodTLSSetBuiltinCA), api.TLSSetBuiltinCARequest{
		Options: api.TLSBuiltinCAOptions{
			Issuer:       "Changed",
			KeyAlgorithm: "ecdsa",
			Curve:        "P384",
		},
	})
	resp, err = session.handleMessage(setReq)
	if err != nil || resp.Error != nil {
		t.Fatalf("set builtin CA response = %#v, err %v", resp, err)
	}
	info, err := api.DecodePayload[api.TLSBuiltinCAInfo](resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if info.Options.Issuer != "Changed" || info.Options.Curve != "P384" {
		t.Fatalf("builtin CA response options = %#v, want Changed P384", info.Options)
	}
	if got := configer.String(cfg.Main, configer.Path{"Config", "TLS", "BuiltinCA", "Issuer"}, ""); got != "Changed" {
		t.Fatalf("config issuer = %q, want Changed", got)
	}
	assertTLSInbox(t, inbox, "policy", "policy")
	assertTLSBuiltinCAEvent(t, events, subResp.SubscriptionID)
	assertTLSEventsContain(t, events, api.TLSChangeCert, tlsBuiltinCACertName, false)
	assertTLSEventsContain(t, events, api.TLSChangePolicy, "policy", false)
}

func TestLoadOSTLSDataUsesReadableCertNames(t *testing.T) {
	pemText := testTLSCAPEM(t, "Example Root CA")
	authorities, err := tlspolicy.ParseAuthoritiesPEM([]byte(pemText))
	if err != nil {
		t.Fatal(err)
	}
	der, err := authorities[0].MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	data, err := loadOSTLSData(context.Background(), tlspolicy.AuthorityFetcherFunc(func(context.Context) ([]tlspolicy.AuthorityCandidate, error) {
		return []tlspolicy.AuthorityCandidate{{
			DER: der,
			Source: tlspolicy.AuthoritySource{
				Name:  "test",
				Trust: tlspolicy.TrustHintAnchor,
			},
		}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	name := tlsOSCertPrefix + "example-root-ca:" + authorities[0].Fingerprint().String()
	cert, ok := data.Certs[name]
	if !ok {
		t.Fatalf("OS TLS cert names = %#v, want %q", data.Certs, name)
	}
	if cert.source != api.TLSSourceOS || cert.PEM != pemText {
		t.Fatalf("OS TLS cert = %#v, want OS source and original PEM", cert)
	}
}

func TestProtectedConfigTLS(t *testing.T) {
	literalPEM := testTLSCAPEM(t, "literal")
	filePEM := testTLSCAPEM(t, "file")
	certPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := writeTestFile(certPath, filePEM); err != nil {
		t.Fatal(err)
	}

	protected := `{
		"TLS": {
			"Certificates": {
				"literal": {"PEM": ` + strconv.Quote(literalPEM) + `},
				"from-file": {"File": ` + strconv.Quote(certPath) + `}
			},
			"Policies": {
				"protected": {
					"trust_anchors": [
						{"cert": "literal", "scope": {"any": true}}
					]
				}
			}
		}
	}`
	cfg, err := ParseConfig([]byte(`{}`), []byte(protected))
	if err != nil {
		t.Fatal(err)
	}
	data, err := cfg.Protected.ImmutableTLS()
	if err != nil {
		t.Fatal(err)
	}
	if got := data.Certs["literal"].PEM; got != literalPEM {
		t.Fatalf("literal TLS PEM mismatch")
	}
	if got := data.Certs["from-file"].PEM; got != filePEM {
		t.Fatalf("file TLS PEM mismatch")
	}
	if got := data.Policies["protected"].Spec.TrustAnchors; len(got) != 1 || got[0].Cert != "literal" {
		t.Fatalf("protected policy = %#v, want literal trust anchor", data.Policies["protected"].Spec)
	}
}

func testTLSPolicy(cert string) api.TLSPolicySpec {
	return api.TLSPolicySpec{
		TrustAnchors: []api.TLSTrustAnchor{{
			Cert:  cert,
			Scope: api.TLSScope{Any: true},
		}},
	}
}

func testTLSCAPEM(t *testing.T, commonName string) string {
	t.Helper()
	certPEM, _ := testTLSCAKeyPairPEM(t, commonName, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	return certPEM
}

func testTLSCAKeyPairPEM(t *testing.T, commonName string, notBefore, notAfter time.Time) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: commonName,
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

func writeTestFile(path, value string) error {
	return os.WriteFile(path, []byte(value), 0o600)
}

func testCertcatExamplePEM(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "certcat", "data", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func findTLSCertEntry(entries []api.TLSCertEntry, name string) *api.TLSCertEntry {
	for i := range entries {
		if entries[i].Name == name {
			return &entries[i]
		}
	}
	return nil
}

func findTLSPolicyEntry(entries []api.TLSPolicyEntry, name string) *api.TLSPolicyEntry {
	for i := range entries {
		if entries[i].Name == name {
			return &entries[i]
		}
	}
	return nil
}

func findTLSTrustAnchor(anchors []api.TLSTrustAnchor, cert string) *api.TLSTrustAnchor {
	for i := range anchors {
		if anchors[i].Cert == cert {
			return &anchors[i]
		}
	}
	return nil
}

func tlsScopeHasDNS(scope api.TLSScope, domain string, includeSubdomains bool) bool {
	for _, rule := range scope.DNS {
		if rule.Domain == domain && rule.IncludeSubdomains == includeSubdomains {
			return true
		}
	}
	return false
}

type tlsInboxNodeClass struct {
	inbox chan almagest.TlsConfigChanged
}

func (c tlsInboxNodeClass) ClassName() string { return "test/TLSInbox" }
func (c tlsInboxNodeClass) ShortDescription() string {
	return "TLS inbox test node"
}
func (c tlsInboxNodeClass) LongDescription() string {
	return "Receives TlsConfigChanged inbox messages."
}
func (c tlsInboxNodeClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{}
}
func (c tlsInboxNodeClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &tlsInboxNode{inbox: c.inbox}, nil
}

type tlsInboxNode struct {
	pasta.BasicNode
	inbox chan almagest.TlsConfigChanged
}

func (n *tlsInboxNode) OnInbox(message pasta.InboxMessage) error {
	if n.inbox == nil {
		return nil
	}
	if payload, ok := message.Payload.(almagest.TlsConfigChanged); ok {
		n.inbox <- payload
	}
	return nil
}

func assertTLSEvent(t *testing.T, events []api.Message, subscriptionID uint64, kind api.TLSChangeKind, name string, deleted bool) {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("TLS events = %d, want 1", len(events))
	}
	if events[0].Method != string(api.EventTLSUpdate) {
		t.Fatalf("TLS event method = %q, want update", events[0].Method)
	}
	var event api.TLSEvent
	if err := json.Unmarshal(events[0].Payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.SubscriptionID != subscriptionID || event.Change.Kind != kind || event.Change.Name != name || event.Change.Deleted != deleted {
		t.Fatalf("TLS event = %#v, want %s %q deleted %v", event, kind, name, deleted)
	}
}

func assertTLSEventsContain(t *testing.T, events []api.Message, kind api.TLSChangeKind, name string, deleted bool) {
	t.Helper()
	for _, msg := range events {
		var event api.TLSEvent
		if err := json.Unmarshal(msg.Payload, &event); err != nil {
			t.Fatal(err)
		}
		if event.Change.Kind == kind && event.Change.Name == name && event.Change.Deleted == deleted {
			return
		}
	}
	t.Fatalf("TLS events %#v do not include %s %q deleted %v", events, kind, name, deleted)
}

func assertTLSBuiltinCAEvent(t *testing.T, events []api.Message, subscriptionID uint64) {
	t.Helper()
	for _, msg := range events {
		var event api.TLSEvent
		if err := json.Unmarshal(msg.Payload, &event); err != nil {
			t.Fatal(err)
		}
		if event.SubscriptionID == subscriptionID &&
			event.Change.Kind == api.TLSChangeBuiltinCA &&
			event.Change.BuiltinCA != nil &&
			event.Change.BuiltinCA.Cert.Name == tlsBuiltinCACertName {
			return
		}
	}
	t.Fatalf("TLS events %#v do not include builtin CA update", events)
}

func assertTLSInbox(t *testing.T, inbox <-chan almagest.TlsConfigChanged, prev, next string) {
	t.Helper()
	select {
	case got := <-inbox:
		if got.NamePrev != prev || got.NameNew != next {
			t.Fatalf("TLS inbox = %#v, want %q -> %q", got, prev, next)
		}
	default:
		t.Fatalf("missing TLS inbox %q -> %q", prev, next)
	}
}

func assertNoTLSInbox(t *testing.T, inbox <-chan almagest.TlsConfigChanged) {
	t.Helper()
	select {
	case got := <-inbox:
		t.Fatalf("unexpected TLS inbox %#v", got)
	default:
	}
}
