package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/gonnect/tlspolicy"
	"github.com/kataras/golog"
	"go.etcd.io/bbolt"
	bberrors "go.etcd.io/bbolt/errors"
)

const (
	tlsCertsBucketName    = "certs"
	tlsPoliciesBucketName = "policies"

	tlsDefaultPolicyName = "default"
	tlsOSCertPrefix      = "os-ca:"
)

type tlsChangeKind string

const (
	tlsChangeCertUpsert   tlsChangeKind = "cert_upsert"
	tlsChangeCertDelete   tlsChangeKind = "cert_delete"
	tlsChangeBuiltinCA    tlsChangeKind = "builtin_ca"
	tlsChangePolicyUpsert tlsChangeKind = "policy_upsert"
	tlsChangePolicyDelete tlsChangeKind = "policy_delete"
)

type tlsChange struct {
	kind tlsChangeKind
	name string
}

// tlsCertificateRecord is the stable JSON shape stored in tls/certs/<name>.
// The PEM must contain exactly one CERTIFICATE block.
type tlsCertificateRecord struct {
	PEM string `json:"pem,omitempty"`
}

type immutableTLSCert struct {
	tlsCertificateRecord
	source api.TLSSource
}

// tlsPolicyRecord is the stable JSON shape stored in tls/policies/<name>.
// Certificate references are resolved against the effective named certificate
// catalog when a policy is compiled.
type tlsPolicyRecord struct {
	Spec api.TLSPolicySpec `json:"spec"`
}

type immutableTLSPolicy struct {
	tlsPolicyRecord
	source api.TLSSource
}

type immutableTLSData struct {
	Certs    map[string]immutableTLSCert
	Policies map[string]immutableTLSPolicy
}

type tlsEffectiveCert struct {
	Name            string
	PEM             string
	Authority       tlspolicy.Authority
	Certificate     *x509.Certificate
	Fingerprint     string
	SPKIFingerprint string
	CertcatMatches  []api.TLSCertcatMatch
	ReadOnly        bool
	Source          api.TLSSource
}

type tlsEffectivePolicy struct {
	Name     string
	Spec     api.TLSPolicySpec
	ReadOnly bool
	Source   api.TLSSource
}

type tlsPolicyNotification struct {
	prevName string
	newName  string
	nodes    []string
}

type tlsMutationResult struct {
	policyNotifications []tlsPolicyNotification
}

func (m *bboltKVManager) ListConfigs() []string {
	if m == nil || m.db == nil {
		return nil
	}
	golog.Debug("TLS storage: listing policy configs")
	policies, err := m.tlsEffectivePolicyMap()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(policies))
	for name := range policies {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (m *bboltKVManager) GetClientConfig(name string, node string) (tls.Config, error) {
	if m == nil || m.db == nil {
		return tls.Config{}, almagest.ErrTlsConfigNotFound
	}
	if !validTLSName(name) || (node != "" && !validSecretNodeName(node)) {
		return tls.Config{}, almagest.ErrTlsConfigInvalidName
	}
	golog.Debugf("TLS storage: client config access policy=%q node=%q", name, node)
	certs, err := m.tlsEffectiveCertMap()
	if err != nil {
		return tls.Config{}, err
	}
	policy, found, err := m.tlsEffectivePolicy(name)
	if err != nil {
		return tls.Config{}, err
	}
	if !found {
		return tls.Config{}, almagest.ErrTlsConfigNotFound
	}
	compiled, err := compileTLSPolicySpec(policy.Spec, certs)
	if err != nil {
		return tls.Config{}, err
	}
	cfg, err := compiled.TLSClientConfig(tlspolicy.ClientTLSOptions{})
	if err != nil {
		return tls.Config{}, err
	}
	if node != "" {
		m.trackTLSPolicyWatcher(name, node)
	}
	return tlsConfigValue(cfg), nil
}

func (m *bboltKVManager) tlsSnapshot() (api.TLSSnapshot, error) {
	golog.Debug("TLS storage: snapshot access")
	certs, err := m.tlsEffectiveCertMap()
	if err != nil {
		return api.TLSSnapshot{}, err
	}
	policies, err := m.tlsEffectivePolicyMap()
	if err != nil {
		return api.TLSSnapshot{}, err
	}

	certEntries := make([]api.TLSCertEntry, 0, len(certs))
	for _, cert := range certs {
		certEntries = append(certEntries, tlsCertEntryToAPI(cert))
	}
	slices.SortFunc(certEntries, func(a, b api.TLSCertEntry) int {
		return strings.Compare(a.Name, b.Name)
	})

	policyEntries := make([]api.TLSPolicyEntry, 0, len(policies))
	for _, policy := range policies {
		policyEntries = append(policyEntries, m.tlsPolicyEntryToAPI(policy, certs))
	}
	slices.SortFunc(policyEntries, func(a, b api.TLSPolicyEntry) int {
		return strings.Compare(a.Name, b.Name)
	})

	return api.TLSSnapshot{
		BuiltinCA: m.tlsBuiltinCAAPIInfo(certs),
		Certs:     certEntries,
		Policies:  policyEntries,
	}, nil
}

func (m *bboltKVManager) tlsCertAPIEntry(name string) (api.TLSCertEntry, bool, error) {
	golog.Debugf("TLS storage: certificate entry access name=%q", name)
	cert, found, err := m.tlsEffectiveCert(name)
	if err != nil || !found {
		return api.TLSCertEntry{}, false, err
	}
	return tlsCertEntryToAPI(cert), true, nil
}

func (m *bboltKVManager) tlsPolicyAPIEntry(name string) (api.TLSPolicyEntry, bool, error) {
	golog.Debugf("TLS storage: policy entry access name=%q", name)
	certs, err := m.tlsEffectiveCertMap()
	if err != nil {
		return api.TLSPolicyEntry{}, false, err
	}
	policy, found, err := m.tlsEffectivePolicy(name)
	if err != nil || !found {
		return api.TLSPolicyEntry{}, false, err
	}
	return m.tlsPolicyEntryToAPI(policy, certs), true, nil
}

func (m *bboltKVManager) setTLSBuiltinCA(ca *tlsBuiltinCA) (tlsMutationResult, error) {
	if ca == nil {
		return tlsMutationResult{}, almagest.ErrTlsConfigInvalidValue
	}
	policies, err := m.tlsEffectivePolicyMap()
	if err != nil {
		return tlsMutationResult{}, err
	}

	m.tlsMu.Lock()
	m.builtinCA = cloneTLSBuiltinCA(ca)
	m.immutableTLS = withTLSBuiltinCA(m.immutableTLS, m.builtinCA)
	m.tlsMu.Unlock()

	policyNames := make([]string, 0, len(policies))
	for name := range policies {
		policyNames = append(policyNames, name)
	}
	slices.Sort(policyNames)

	changes := []tlsChange{
		{kind: tlsChangeBuiltinCA, name: tlsBuiltinCACertName},
		{kind: tlsChangeCertUpsert, name: tlsBuiltinCACertName},
	}
	notifications := make([]tlsPolicyNotification, 0, len(policyNames))
	for _, name := range policyNames {
		changes = append(changes, tlsChange{kind: tlsChangePolicyUpsert, name: name})
		notifications = append(notifications, m.tlsPolicyNotification(name, name))
	}
	golog.Debugf("builtin TLS CA: activated certificate and queued %d TLS policy rebuild notifications", len(notifications))
	m.notifyTLSChange(changes...)
	return tlsMutationResult{policyNotifications: notifications}, nil
}

// setTLSOSData replaces the read-only OS certificate overlay in memory. It
// preserves protected-config certificates and the daemon built-in CA, then
// rebuilds the generated default policy from the new OS roots.
func (m *bboltKVManager) setTLSOSData(data immutableTLSData) (tlsMutationResult, error) {
	nextOSCerts := map[string]immutableTLSCert{}
	for name, cert := range data.Certs {
		if !validTLSName(name) || !strings.HasPrefix(name, tlsOSCertPrefix) {
			return tlsMutationResult{}, almagest.ErrTlsConfigInvalidName
		}
		if _, _, err := parseTLSAuthorityPEM(cert.PEM); err != nil {
			return tlsMutationResult{}, fmt.Errorf("%w: OS TLS certificate %q: %v", almagest.ErrTlsConfigInvalidValue, name, err)
		}
		nextOSCerts[name] = immutableTLSCert{
			tlsCertificateRecord: tlsCertificateRecord{PEM: cert.PEM},
			source:               api.TLSSourceOS,
		}
	}

	oldPolicies, err := m.tlsEffectivePolicyMap()
	if err != nil {
		return tlsMutationResult{}, err
	}
	oldOSCerts := map[string]immutableTLSCert{}
	nextVisibleOSCerts := map[string]immutableTLSCert{}

	m.tlsMu.Lock()
	next := cloneImmutableTLSDataWithoutDefault(m.immutableTLS)
	if next.Certs == nil {
		next.Certs = map[string]immutableTLSCert{}
	}
	for name, cert := range next.Certs {
		if cert.source == api.TLSSourceOS {
			oldOSCerts[name] = cert
			delete(next.Certs, name)
		}
	}
	for name, cert := range nextOSCerts {
		if existing, ok := next.Certs[name]; ok && existing.source != api.TLSSourceOS {
			continue
		}
		next.Certs[name] = cert
		nextVisibleOSCerts[name] = cert
	}
	m.immutableTLS = ensureDefaultTLSPolicy(next)
	m.tlsMu.Unlock()

	newPolicies, err := m.tlsEffectivePolicyMap()
	if err != nil {
		return tlsMutationResult{}, err
	}

	deleted, upserted := diffTLSOSCerts(oldOSCerts, nextVisibleOSCerts)
	changedCerts := map[string]struct{}{}
	changes := make([]tlsChange, 0, len(deleted)+len(upserted))
	for _, name := range deleted {
		changedCerts[name] = struct{}{}
		changes = append(changes, tlsChange{kind: tlsChangeCertDelete, name: name})
	}
	for _, name := range upserted {
		changedCerts[name] = struct{}{}
		changes = append(changes, tlsChange{kind: tlsChangeCertUpsert, name: name})
	}

	changedPolicies := tlsPoliciesAffectedByCerts(oldPolicies, newPolicies, changedCerts)
	notifications := makeTLSCertPolicyNotifications(m, changedPolicies, &changes)
	golog.Debugf("TLS OS certificate reload: %d deleted, %d added or changed, %d policies queued",
		len(deleted), len(upserted), len(notifications))
	m.notifyTLSChange(changes...)
	return tlsMutationResult{policyNotifications: notifications}, nil
}

func (m *bboltKVManager) setTLSCert(name, pemText string) (tlsMutationResult, error) {
	if !validMutableTLSCertName(name) {
		return tlsMutationResult{}, almagest.ErrTlsConfigInvalidName
	}
	if _, ok := m.immutableTLSCert(name); ok {
		return tlsMutationResult{}, almagest.ErrTlsConfigReadOnly
	}
	record := tlsCertificateRecord{PEM: pemText}
	if _, _, err := parseTLSAuthorityPEM(record.PEM); err != nil {
		return tlsMutationResult{}, fmt.Errorf("%w: %v", almagest.ErrTlsConfigInvalidValue, err)
	}
	golog.Debugf("TLS storage: writing mutable certificate name=%q", name)
	affected, err := m.tlsPoliciesMentioningCert(name)
	if err != nil {
		return tlsMutationResult{}, err
	}
	raw, err := encodeTLSCertificateRecord(record)
	if err != nil {
		return tlsMutationResult{}, err
	}
	err = mapBboltTLSError(m.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := mutableTLSCertsBucket(tx, true)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(name), raw)
	}))
	if err != nil {
		return tlsMutationResult{}, err
	}

	changes := []tlsChange{{kind: tlsChangeCertUpsert, name: name}}
	notifications := make([]tlsPolicyNotification, 0, len(affected))
	for _, policyName := range affected {
		changes = append(changes, tlsChange{kind: tlsChangePolicyUpsert, name: policyName})
		notifications = append(notifications, m.tlsPolicyNotification(policyName, policyName))
	}
	m.notifyTLSChange(changes...)
	return tlsMutationResult{policyNotifications: notifications}, nil
}

func (m *bboltKVManager) renameTLSCert(name, newName string) (tlsMutationResult, error) {
	if !validMutableTLSCertName(name) || !validMutableTLSCertName(newName) {
		return tlsMutationResult{}, almagest.ErrTlsConfigInvalidName
	}
	golog.Debugf("TLS storage: renaming mutable certificate name=%q new_name=%q", name, newName)
	if name == newName {
		cert, found, err := m.tlsEffectiveCert(name)
		if err != nil {
			return tlsMutationResult{}, err
		}
		if !found {
			return tlsMutationResult{}, errTLSCertNotFound
		}
		if cert.ReadOnly {
			return tlsMutationResult{}, almagest.ErrTlsConfigReadOnly
		}
		return tlsMutationResult{}, nil
	}
	if _, ok := m.immutableTLSCert(name); ok {
		return tlsMutationResult{}, almagest.ErrTlsConfigReadOnly
	}
	if _, ok := m.immutableTLSCert(newName); ok {
		return tlsMutationResult{}, almagest.ErrTlsConfigConflict
	}

	changedPolicies := map[string]struct{}{}
	err := mapBboltTLSError(m.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := mutableTLSCertsBucket(tx, false)
		if err != nil || bucket == nil {
			if err != nil {
				return err
			}
			return errTLSCertNotFound
		}
		if bucket.Get([]byte(newName)) != nil {
			return almagest.ErrTlsConfigConflict
		}
		raw := bucket.Get([]byte(name))
		if raw == nil {
			return errTLSCertNotFound
		}
		if err := bucket.Put([]byte(newName), append([]byte(nil), raw...)); err != nil {
			return err
		}
		if err := bucket.Delete([]byte(name)); err != nil {
			return err
		}
		return updateMutableTLSPoliciesInTx(tx, func(spec api.TLSPolicySpec) (api.TLSPolicySpec, bool) {
			return renameTLSCertInPolicy(spec, name, newName)
		}, changedPolicies)
	}))
	if err != nil {
		return tlsMutationResult{}, err
	}
	m.updateImmutableTLSPolicies(func(spec api.TLSPolicySpec) (api.TLSPolicySpec, bool) {
		return renameTLSCertInPolicy(spec, name, newName)
	}, changedPolicies)

	changes := []tlsChange{
		{kind: tlsChangeCertDelete, name: name},
		{kind: tlsChangeCertUpsert, name: newName},
	}
	notifications := makeTLSCertPolicyNotifications(m, changedPolicies, &changes)
	m.notifyTLSChange(changes...)
	return tlsMutationResult{policyNotifications: notifications}, nil
}

func (m *bboltKVManager) deleteTLSCert(name string) (tlsMutationResult, error) {
	if !validMutableTLSCertName(name) {
		return tlsMutationResult{}, almagest.ErrTlsConfigInvalidName
	}
	if _, ok := m.immutableTLSCert(name); ok {
		return tlsMutationResult{}, almagest.ErrTlsConfigReadOnly
	}
	golog.Debugf("TLS storage: deleting mutable certificate name=%q", name)

	deleted := false
	changedPolicies := map[string]struct{}{}
	err := mapBboltTLSError(m.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := mutableTLSCertsBucket(tx, false)
		if err != nil || bucket == nil {
			return err
		}
		if bucket.Get([]byte(name)) != nil {
			if err := bucket.Delete([]byte(name)); err != nil {
				return err
			}
			deleted = true
		}
		return updateMutableTLSPoliciesInTx(tx, func(spec api.TLSPolicySpec) (api.TLSPolicySpec, bool) {
			return deleteTLSCertFromPolicy(spec, name)
		}, changedPolicies)
	}))
	if err != nil {
		return tlsMutationResult{}, err
	}
	m.updateImmutableTLSPolicies(func(spec api.TLSPolicySpec) (api.TLSPolicySpec, bool) {
		return deleteTLSCertFromPolicy(spec, name)
	}, changedPolicies)

	var changes []tlsChange
	if deleted {
		changes = append(changes, tlsChange{kind: tlsChangeCertDelete, name: name})
	}
	notifications := makeTLSCertPolicyNotifications(m, changedPolicies, &changes)
	m.notifyTLSChange(changes...)
	return tlsMutationResult{policyNotifications: notifications}, nil
}

func (m *bboltKVManager) setTLSPolicy(name string, spec api.TLSPolicySpec) (tlsMutationResult, error) {
	if !validMutableTLSPolicyName(name) {
		return tlsMutationResult{}, almagest.ErrTlsConfigInvalidName
	}
	if _, ok := m.immutableTLSPolicy(name); ok {
		return tlsMutationResult{}, almagest.ErrTlsConfigReadOnly
	}
	spec = normalizeTLSPolicySpec(spec)
	certs, err := m.tlsEffectiveCertMap()
	if err != nil {
		return tlsMutationResult{}, err
	}
	if _, err := compileTLSPolicySpec(spec, certs); err != nil {
		return tlsMutationResult{}, fmt.Errorf("%w: %v", almagest.ErrTlsConfigInvalidValue, err)
	}
	golog.Debugf("TLS storage: writing mutable policy name=%q", name)
	raw, err := encodeTLSPolicyRecord(tlsPolicyRecord{Spec: spec})
	if err != nil {
		return tlsMutationResult{}, err
	}
	err = mapBboltTLSError(m.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := mutableTLSPoliciesBucket(tx, true)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(name), raw)
	}))
	if err != nil {
		return tlsMutationResult{}, err
	}
	m.notifyTLSChange(tlsChange{kind: tlsChangePolicyUpsert, name: name})
	return tlsMutationResult{
		policyNotifications: []tlsPolicyNotification{m.tlsPolicyNotification(name, name)},
	}, nil
}

func (m *bboltKVManager) renameTLSPolicy(name, newName string) (tlsMutationResult, error) {
	if !validMutableTLSPolicyName(name) || !validMutableTLSPolicyName(newName) {
		return tlsMutationResult{}, almagest.ErrTlsConfigInvalidName
	}
	golog.Debugf("TLS storage: renaming mutable policy name=%q new_name=%q", name, newName)
	if name == newName {
		policy, found, err := m.tlsEffectivePolicy(name)
		if err != nil {
			return tlsMutationResult{}, err
		}
		if !found {
			return tlsMutationResult{}, errTLSPolicyNotFound
		}
		if policy.ReadOnly {
			return tlsMutationResult{}, almagest.ErrTlsConfigReadOnly
		}
		return tlsMutationResult{}, nil
	}
	if _, ok := m.immutableTLSPolicy(name); ok {
		return tlsMutationResult{}, almagest.ErrTlsConfigReadOnly
	}
	if _, ok := m.immutableTLSPolicy(newName); ok {
		return tlsMutationResult{}, almagest.ErrTlsConfigConflict
	}

	err := mapBboltTLSError(m.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := mutableTLSPoliciesBucket(tx, false)
		if err != nil || bucket == nil {
			if err != nil {
				return err
			}
			return errTLSPolicyNotFound
		}
		if bucket.Get([]byte(newName)) != nil {
			return almagest.ErrTlsConfigConflict
		}
		raw := bucket.Get([]byte(name))
		if raw == nil {
			return errTLSPolicyNotFound
		}
		if err := bucket.Put([]byte(newName), append([]byte(nil), raw...)); err != nil {
			return err
		}
		return bucket.Delete([]byte(name))
	}))
	if err != nil {
		return tlsMutationResult{}, err
	}

	nodes := m.renameTLSPolicyWatchers(name, newName)
	m.notifyTLSChange(
		tlsChange{kind: tlsChangePolicyDelete, name: name},
		tlsChange{kind: tlsChangePolicyUpsert, name: newName},
	)
	return tlsMutationResult{
		policyNotifications: []tlsPolicyNotification{{
			prevName: name,
			newName:  newName,
			nodes:    nodes,
		}},
	}, nil
}

func (m *bboltKVManager) deleteTLSPolicy(name string) (tlsMutationResult, error) {
	if !validMutableTLSPolicyName(name) {
		return tlsMutationResult{}, almagest.ErrTlsConfigInvalidName
	}
	if _, ok := m.immutableTLSPolicy(name); ok {
		return tlsMutationResult{}, almagest.ErrTlsConfigReadOnly
	}
	golog.Debugf("TLS storage: deleting mutable policy name=%q", name)

	deleted := false
	err := mapBboltTLSError(m.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := mutableTLSPoliciesBucket(tx, false)
		if err != nil || bucket == nil {
			return err
		}
		if bucket.Get([]byte(name)) == nil {
			return nil
		}
		if err := bucket.Delete([]byte(name)); err != nil {
			return err
		}
		deleted = true
		return nil
	}))
	if err != nil {
		return tlsMutationResult{}, err
	}
	nodes := m.deleteTLSPolicyWatchers(name)
	if deleted {
		m.notifyTLSChange(tlsChange{kind: tlsChangePolicyDelete, name: name})
	}
	return tlsMutationResult{
		policyNotifications: []tlsPolicyNotification{{
			prevName: name,
			newName:  name,
			nodes:    nodes,
		}},
	}, nil
}

func (m *bboltKVManager) tlsEffectiveCert(name string) (tlsEffectiveCert, bool, error) {
	if !validTLSName(name) {
		return tlsEffectiveCert{}, false, almagest.ErrTlsConfigInvalidName
	}
	if record, ok := m.immutableTLSCert(name); ok {
		cert, err := tlsEffectiveCertFromRecord(name, record.tlsCertificateRecord, true, record.source)
		return cert, err == nil, err
	}
	var out tlsEffectiveCert
	err := mapBboltTLSError(m.db.View(func(tx *bbolt.Tx) error {
		bucket, err := mutableTLSCertsBucket(tx, false)
		if err != nil || bucket == nil {
			return err
		}
		raw := bucket.Get([]byte(name))
		if raw == nil {
			return nil
		}
		record, err := decodeTLSCertificateRecord(raw)
		if err != nil {
			return err
		}
		out, err = tlsEffectiveCertFromRecord(name, record, false, api.TLSSourceMutable)
		return err
	}))
	if err != nil {
		return tlsEffectiveCert{}, false, err
	}
	return out, out.Name != "", nil
}

func (m *bboltKVManager) tlsEffectiveCertMap() (map[string]tlsEffectiveCert, error) {
	immutable := m.cloneImmutableTLSCerts()
	out := make(map[string]tlsEffectiveCert, len(immutable))
	err := mapBboltTLSError(m.db.View(func(tx *bbolt.Tx) error {
		bucket, err := mutableTLSCertsBucket(tx, false)
		if err != nil || bucket == nil {
			return err
		}
		return bucket.ForEach(func(k, v []byte) error {
			name := string(k)
			if _, shadowed := immutable[name]; shadowed {
				return nil
			}
			record, err := decodeTLSCertificateRecord(v)
			if err != nil {
				return err
			}
			cert, err := tlsEffectiveCertFromRecord(name, record, false, api.TLSSourceMutable)
			if err != nil {
				return err
			}
			out[name] = cert
			return nil
		})
	}))
	if err != nil {
		return nil, err
	}
	for name, record := range immutable {
		cert, err := tlsEffectiveCertFromRecord(name, record.tlsCertificateRecord, true, record.source)
		if err != nil {
			return nil, err
		}
		out[name] = cert
	}
	return out, nil
}

func (m *bboltKVManager) tlsEffectivePolicy(name string) (tlsEffectivePolicy, bool, error) {
	if !validTLSName(name) {
		return tlsEffectivePolicy{}, false, almagest.ErrTlsConfigInvalidName
	}
	if record, ok := m.immutableTLSPolicy(name); ok {
		return tlsEffectivePolicy{
			Name:     name,
			Spec:     normalizeTLSPolicySpec(record.Spec),
			ReadOnly: true,
			Source:   record.source,
		}, true, nil
	}
	var out tlsEffectivePolicy
	err := mapBboltTLSError(m.db.View(func(tx *bbolt.Tx) error {
		bucket, err := mutableTLSPoliciesBucket(tx, false)
		if err != nil || bucket == nil {
			return err
		}
		raw := bucket.Get([]byte(name))
		if raw == nil {
			return nil
		}
		record, err := decodeTLSPolicyRecord(raw)
		if err != nil {
			return err
		}
		out = tlsEffectivePolicy{
			Name:     name,
			Spec:     normalizeTLSPolicySpec(record.Spec),
			ReadOnly: false,
			Source:   api.TLSSourceMutable,
		}
		return nil
	}))
	if err != nil {
		return tlsEffectivePolicy{}, false, err
	}
	return out, out.Name != "", nil
}

func (m *bboltKVManager) tlsEffectivePolicyMap() (map[string]tlsEffectivePolicy, error) {
	immutable := m.cloneImmutableTLSPolicies()
	out := make(map[string]tlsEffectivePolicy, len(immutable))
	err := mapBboltTLSError(m.db.View(func(tx *bbolt.Tx) error {
		bucket, err := mutableTLSPoliciesBucket(tx, false)
		if err != nil || bucket == nil {
			return err
		}
		return bucket.ForEach(func(k, v []byte) error {
			name := string(k)
			if _, shadowed := immutable[name]; shadowed {
				return nil
			}
			record, err := decodeTLSPolicyRecord(v)
			if err != nil {
				return err
			}
			out[name] = tlsEffectivePolicy{
				Name:     name,
				Spec:     normalizeTLSPolicySpec(record.Spec),
				ReadOnly: false,
				Source:   api.TLSSourceMutable,
			}
			return nil
		})
	}))
	if err != nil {
		return nil, err
	}
	for name, record := range immutable {
		out[name] = tlsEffectivePolicy{
			Name:     name,
			Spec:     normalizeTLSPolicySpec(record.Spec),
			ReadOnly: true,
			Source:   record.source,
		}
	}
	return out, nil
}

func (m *bboltKVManager) tlsPoliciesMentioningCert(certName string) ([]string, error) {
	policies, err := m.tlsEffectivePolicyMap()
	if err != nil {
		return nil, err
	}
	var out []string
	for name, policy := range policies {
		if tlsPolicyMentionsCert(policy.Spec, certName) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (m *bboltKVManager) tlsPolicyEntryToAPI(policy tlsEffectivePolicy, certs map[string]tlsEffectiveCert) api.TLSPolicyEntry {
	spec := tlsPolicySpecWithBuiltinCA(policy.Spec, certs)
	entry := api.TLSPolicyEntry{
		Name:     policy.Name,
		Spec:     spec,
		ReadOnly: policy.ReadOnly,
		Source:   policy.Source,
		Watchers: m.tlsPolicyWatchers(policy.Name),
	}
	if _, err := compileTLSPolicySpec(spec, certs); err != nil {
		entry.Valid = false
		entry.Error = err.Error()
		return entry
	}
	entry.Valid = true
	return entry
}

func (m *bboltKVManager) tlsBuiltinCAAPIInfo(certs map[string]tlsEffectiveCert) *api.TLSBuiltinCAInfo {
	m.tlsMu.Lock()
	ca := cloneTLSBuiltinCA(m.builtinCA)
	m.tlsMu.Unlock()
	if ca == nil {
		return nil
	}
	cert, ok := certs[tlsBuiltinCACertName]
	if !ok {
		return nil
	}
	info := &api.TLSBuiltinCAInfo{
		Cert:    tlsCertEntryToAPI(cert),
		Options: ca.options,
	}
	if ca.immutable {
		info.Immutable = true
		info.RegenerationDisabled = true
		info.RegenerationDisabledBy = "protected_config"
	}
	return info
}

func (m *bboltKVManager) currentTLSBuiltinCA() *tlsBuiltinCA {
	m.tlsMu.Lock()
	defer m.tlsMu.Unlock()
	return cloneTLSBuiltinCA(m.builtinCA)
}

func tlsCertEntryToAPI(cert tlsEffectiveCert) api.TLSCertEntry {
	return api.TLSCertEntry{
		Name:            cert.Name,
		PEM:             cert.PEM,
		Fingerprint:     cert.Fingerprint,
		SPKIFingerprint: cert.SPKIFingerprint,
		Subject:         cert.Certificate.Subject.String(),
		Issuer:          cert.Certificate.Issuer.String(),
		SerialNumber:    cert.Certificate.SerialNumber.Text(16),
		NotBefore:       cert.Certificate.NotBefore.UTC().Format(time.RFC3339Nano),
		NotAfter:        cert.Certificate.NotAfter.UTC().Format(time.RFC3339Nano),
		IsCA:            cert.Certificate.IsCA,
		ReadOnly:        cert.ReadOnly,
		Source:          cert.Source,
		CertcatMatches:  cert.CertcatMatches,
	}
}

func tlsEffectiveCertFromRecord(name string, record tlsCertificateRecord, readOnly bool, source api.TLSSource) (tlsEffectiveCert, error) {
	if !validTLSName(name) {
		return tlsEffectiveCert{}, almagest.ErrTlsConfigInvalidName
	}
	authority, cert, err := parseTLSAuthorityPEM(record.PEM)
	if err != nil {
		return tlsEffectiveCert{}, fmt.Errorf("%w: %v", almagest.ErrTlsConfigInvalidValue, err)
	}
	return tlsEffectiveCert{
		Name:            name,
		PEM:             record.PEM,
		Authority:       authority,
		Certificate:     cert,
		Fingerprint:     authority.Fingerprint().String(),
		SPKIFingerprint: authority.SPKIFingerprint().String(),
		CertcatMatches:  tlsCertcatMatchesAPI(tlsCertcatMatches(cert)),
		ReadOnly:        readOnly,
		Source:          source,
	}, nil
}

func compileTLSPolicySpec(spec api.TLSPolicySpec, certs map[string]tlsEffectiveCert) (*tlspolicy.ServerPolicy, error) {
	spec = tlsPolicySpecWithBuiltinCA(spec, certs)
	compiled := tlspolicy.ServerPolicySpec{
		TrustAnchors: make([]tlspolicy.ScopedAuthority, 0, len(spec.TrustAnchors)),
		Constraints:  make([]tlspolicy.AuthorityConstraint, 0, len(spec.Constraints)),
		Pins:         make([]tlspolicy.ScopedPin, 0, len(spec.Pins)),
	}
	for i, anchor := range spec.TrustAnchors {
		cert, ok := certs[anchor.Cert]
		if !ok {
			return nil, fmt.Errorf("tls policy trust anchor %d references unknown cert %q", i, anchor.Cert)
		}
		scope, err := tlsScopeFromAPI(anchor.Scope)
		if err != nil {
			return nil, fmt.Errorf("tls policy trust anchor %d scope: %w", i, err)
		}
		compiled.TrustAnchors = append(compiled.TrustAnchors, tlspolicy.ScopedAuthority{
			Authority: cert.Authority,
			Scope:     scope,
		})
	}
	for i, constraint := range spec.Constraints {
		cert, ok := certs[constraint.Cert]
		if !ok {
			return nil, fmt.Errorf("tls policy constraint %d references unknown cert %q", i, constraint.Cert)
		}
		match, err := tlsAuthorityMatchFromAPI(constraint.Match)
		if err != nil {
			return nil, fmt.Errorf("tls policy constraint %d match: %w", i, err)
		}
		scope, err := tlsScopeFromAPI(constraint.Scope)
		if err != nil {
			return nil, fmt.Errorf("tls policy constraint %d scope: %w", i, err)
		}
		compiled.Constraints = append(compiled.Constraints, tlspolicy.AuthorityConstraint{
			Authority: cert.Authority,
			Match:     match,
			Scope:     scope,
		})
	}
	for i, scopedPin := range spec.Pins {
		pin, err := tlsPinFromAPI(scopedPin)
		if err != nil {
			return nil, fmt.Errorf("tls policy pin %d: %w", i, err)
		}
		scope, err := tlsScopeFromAPI(scopedPin.Scope)
		if err != nil {
			return nil, fmt.Errorf("tls policy pin %d scope: %w", i, err)
		}
		compiled.Pins = append(compiled.Pins, tlspolicy.ScopedPin{
			Pin:   pin,
			Scope: scope,
		})
	}
	return tlspolicy.CompileServerPolicy(compiled)
}

func tlsPolicySpecWithBuiltinCA(spec api.TLSPolicySpec, certs map[string]tlsEffectiveCert) api.TLSPolicySpec {
	if _, ok := certs[tlsBuiltinCACertName]; ok {
		spec.TrustAnchors = append(spec.TrustAnchors, api.TLSTrustAnchor{
			Cert:  tlsBuiltinCACertName,
			Scope: api.TLSScope{Any: true},
		})
	}
	return normalizeTLSPolicySpec(spec)
}

func tlsAuthorityMatchFromAPI(value string) (tlspolicy.AuthorityMatch, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "certificate":
		return tlspolicy.MatchCertificate, nil
	case "spki":
		return tlspolicy.MatchSPKI, nil
	default:
		return 0, fmt.Errorf("unsupported authority match %q", value)
	}
}

func tlsPinFromAPI(value api.TLSPin) (tlspolicy.Pin, error) {
	var kind tlspolicy.PinKind
	switch strings.ToLower(strings.TrimSpace(value.Kind)) {
	case "certificate":
		kind = tlspolicy.PinLeafCertificate
	case "spki":
		kind = tlspolicy.PinLeafSPKI
	default:
		return tlspolicy.Pin{}, fmt.Errorf("unsupported pin kind %q", value.Kind)
	}
	fingerprint, err := tlspolicy.ParseFingerprint(value.Fingerprint)
	if err != nil {
		return tlspolicy.Pin{}, err
	}
	return tlspolicy.NewPin(kind, fingerprint)
}

func tlsScopeFromAPI(value api.TLSScope) (tlspolicy.Scope, error) {
	if value.Any {
		if len(value.DNS) != 0 || len(value.IPPrefixes) != 0 {
			return tlspolicy.Scope{}, errors.New("any scope cannot include DNS or IP rules")
		}
		return tlspolicy.AnyServerScope(), nil
	}
	var out tlspolicy.Scope
	for _, rule := range value.DNS {
		scope, err := tlspolicy.NewDNSDomainScope(rule.Domain, rule.IncludeSubdomains)
		if err != nil {
			return tlspolicy.Scope{}, err
		}
		out.DNS = append(out.DNS, scope.DNS...)
	}
	for _, raw := range value.IPPrefixes {
		prefix, err := parseTLSPrefix(raw)
		if err != nil {
			return tlspolicy.Scope{}, err
		}
		scope, err := tlspolicy.NewIPPrefixScope(prefix)
		if err != nil {
			return tlspolicy.Scope{}, err
		}
		out.IPPrefixes = append(out.IPPrefixes, scope.IPPrefixes...)
	}
	if err := out.Validate(); err != nil {
		return tlspolicy.Scope{}, err
	}
	return out, nil
}

func parseTLSPrefix(value string) (netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix, nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Prefix{}, err
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func parseTLSAuthorityPEM(pemText string) (tlspolicy.Authority, *x509.Certificate, error) {
	if !utf8.ValidString(pemText) {
		return tlspolicy.Authority{}, nil, errors.New("certificate PEM is not valid UTF-8")
	}
	authorities, err := tlspolicy.ParseAuthoritiesPEM([]byte(pemText))
	if err != nil {
		return tlspolicy.Authority{}, nil, err
	}
	if len(authorities) != 1 {
		return tlspolicy.Authority{}, nil, fmt.Errorf("certificate PEM contains %d certificates; want 1", len(authorities))
	}
	cert, err := authorities[0].Certificate()
	if err != nil {
		return tlspolicy.Authority{}, nil, err
	}
	return authorities[0], cert, nil
}

func tlsPEMFromDER(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: append([]byte(nil), der...),
	}))
}

func tlsConfigValue(in *tls.Config) tls.Config {
	if in == nil {
		return tls.Config{}
	}
	return tls.Config{
		Rand:                                in.Rand,
		Time:                                in.Time,
		Certificates:                        cloneTLSCertificates(in.Certificates),
		GetCertificate:                      in.GetCertificate,
		GetClientCertificate:                in.GetClientCertificate,
		GetConfigForClient:                  in.GetConfigForClient,
		VerifyPeerCertificate:               in.VerifyPeerCertificate,
		VerifyConnection:                    in.VerifyConnection,
		RootCAs:                             in.RootCAs,
		NextProtos:                          append([]string(nil), in.NextProtos...),
		ServerName:                          in.ServerName,
		ClientAuth:                          in.ClientAuth,
		ClientCAs:                           in.ClientCAs,
		InsecureSkipVerify:                  in.InsecureSkipVerify,
		CipherSuites:                        append([]uint16(nil), in.CipherSuites...),
		SessionTicketsDisabled:              in.SessionTicketsDisabled,
		ClientSessionCache:                  in.ClientSessionCache,
		UnwrapSession:                       in.UnwrapSession,
		WrapSession:                         in.WrapSession,
		MinVersion:                          in.MinVersion,
		MaxVersion:                          in.MaxVersion,
		CurvePreferences:                    append([]tls.CurveID(nil), in.CurvePreferences...),
		DynamicRecordSizingDisabled:         in.DynamicRecordSizingDisabled,
		Renegotiation:                       in.Renegotiation,
		KeyLogWriter:                        in.KeyLogWriter,
		EncryptedClientHelloConfigList:      append([]byte(nil), in.EncryptedClientHelloConfigList...),
		EncryptedClientHelloRejectionVerify: in.EncryptedClientHelloRejectionVerify,
		GetEncryptedClientHelloKeys:         in.GetEncryptedClientHelloKeys,
		EncryptedClientHelloKeys:            append([]tls.EncryptedClientHelloKey(nil), in.EncryptedClientHelloKeys...),
	}
}

func cloneTLSCertificates(in []tls.Certificate) []tls.Certificate {
	if len(in) == 0 {
		return nil
	}
	out := make([]tls.Certificate, len(in))
	for i, cert := range in {
		out[i] = cloneTLSCertificate(cert)
	}
	return out
}

func cloneTLSCertificate(in tls.Certificate) tls.Certificate {
	out := tls.Certificate{
		Certificate:                  cloneTLSByteSlices(in.Certificate),
		PrivateKey:                   in.PrivateKey,
		SupportedSignatureAlgorithms: append([]tls.SignatureScheme(nil), in.SupportedSignatureAlgorithms...),
		OCSPStaple:                   append([]byte(nil), in.OCSPStaple...),
		SignedCertificateTimestamps:  cloneTLSByteSlices(in.SignedCertificateTimestamps),
		Leaf:                         in.Leaf,
	}
	return out
}

func cloneTLSByteSlices(in [][]byte) [][]byte {
	if len(in) == 0 {
		return nil
	}
	out := make([][]byte, len(in))
	for i, value := range in {
		out[i] = append([]byte(nil), value...)
	}
	return out
}

func normalizeTLSPolicySpec(spec api.TLSPolicySpec) api.TLSPolicySpec {
	spec.TrustAnchors = append([]api.TLSTrustAnchor(nil), spec.TrustAnchors...)
	spec.Constraints = append([]api.TLSAuthorityConstraint(nil), spec.Constraints...)
	spec.Pins = append([]api.TLSPin(nil), spec.Pins...)

	anchors := map[string]api.TLSScope{}
	for _, anchor := range spec.TrustAnchors {
		anchors[anchor.Cert] = mergeTLSScope(anchors[anchor.Cert], normalizeTLSScope(anchor.Scope))
	}
	anchorNames := make([]string, 0, len(anchors))
	for name := range anchors {
		anchorNames = append(anchorNames, name)
	}
	slices.Sort(anchorNames)
	spec.TrustAnchors = spec.TrustAnchors[:0]
	for _, name := range anchorNames {
		spec.TrustAnchors = append(spec.TrustAnchors, api.TLSTrustAnchor{
			Cert:  name,
			Scope: anchors[name],
		})
	}

	constraints := map[string]api.TLSAuthorityConstraint{}
	for _, constraint := range spec.Constraints {
		match := normalizeTLSAuthorityMatchText(constraint.Match)
		key := constraint.Cert + "\x00" + match
		current := constraints[key]
		current.Cert = constraint.Cert
		current.Match = match
		current.Scope = mergeTLSScope(current.Scope, normalizeTLSScope(constraint.Scope))
		constraints[key] = current
	}
	constraintKeys := make([]string, 0, len(constraints))
	for key := range constraints {
		constraintKeys = append(constraintKeys, key)
	}
	slices.Sort(constraintKeys)
	spec.Constraints = spec.Constraints[:0]
	for _, key := range constraintKeys {
		spec.Constraints = append(spec.Constraints, constraints[key])
	}

	pins := map[string]api.TLSPin{}
	for _, pin := range spec.Pins {
		kind := normalizeTLSPinKindText(pin.Kind)
		fingerprint := normalizeTLSFingerprintText(pin.Fingerprint)
		key := kind + "\x00" + fingerprint
		current := pins[key]
		current.Kind = kind
		current.Fingerprint = fingerprint
		current.Scope = mergeTLSScope(current.Scope, normalizeTLSScope(pin.Scope))
		pins[key] = current
	}
	pinKeys := make([]string, 0, len(pins))
	for key := range pins {
		pinKeys = append(pinKeys, key)
	}
	slices.Sort(pinKeys)
	spec.Pins = spec.Pins[:0]
	for _, key := range pinKeys {
		spec.Pins = append(spec.Pins, pins[key])
	}
	return spec
}

func normalizeTLSScope(scope api.TLSScope) api.TLSScope {
	if scope.Any {
		return api.TLSScope{Any: true}
	}
	if len(scope.DNS) > 0 {
		dns := append([]api.TLSDNSRule(nil), scope.DNS...)
		for i := range dns {
			dns[i].Domain = strings.TrimSpace(dns[i].Domain)
		}
		slices.SortFunc(dns, func(a, b api.TLSDNSRule) int {
			if a.Domain != b.Domain {
				return strings.Compare(a.Domain, b.Domain)
			}
			return compareBool(a.IncludeSubdomains, b.IncludeSubdomains)
		})
		scope.DNS = slices.Compact(dns)
	}
	if len(scope.IPPrefixes) > 0 {
		prefixes := append([]string(nil), scope.IPPrefixes...)
		for i := range prefixes {
			prefixes[i] = strings.TrimSpace(prefixes[i])
		}
		slices.Sort(prefixes)
		scope.IPPrefixes = slices.Compact(prefixes)
	}
	return scope
}

func mergeTLSScope(left, right api.TLSScope) api.TLSScope {
	if left.Any || right.Any {
		return api.TLSScope{Any: true}
	}
	left.DNS = append(left.DNS, right.DNS...)
	left.IPPrefixes = append(left.IPPrefixes, right.IPPrefixes...)
	return normalizeTLSScope(left)
}

func normalizeTLSAuthorityMatchText(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "certificate":
		return "certificate"
	case "spki":
		return "spki"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeTLSPinKindText(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "certificate":
		return "certificate"
	case "spki":
		return "spki"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeTLSFingerprintText(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), ":", ""))
}

func compareBool(a, b bool) int {
	if a == b {
		return 0
	}
	if !a {
		return -1
	}
	return 1
}

func renameTLSCertInPolicy(spec api.TLSPolicySpec, oldName, newName string) (api.TLSPolicySpec, bool) {
	changed := false
	for i := range spec.TrustAnchors {
		if spec.TrustAnchors[i].Cert == oldName {
			spec.TrustAnchors[i].Cert = newName
			changed = true
		}
	}
	for i := range spec.Constraints {
		if spec.Constraints[i].Cert == oldName {
			spec.Constraints[i].Cert = newName
			changed = true
		}
	}
	if !changed {
		return api.TLSPolicySpec{}, false
	}
	return normalizeTLSPolicySpec(spec), true
}

func deleteTLSCertFromPolicy(spec api.TLSPolicySpec, name string) (api.TLSPolicySpec, bool) {
	anchors := spec.TrustAnchors[:0]
	changed := false
	for _, anchor := range spec.TrustAnchors {
		if anchor.Cert == name {
			changed = true
			continue
		}
		anchors = append(anchors, anchor)
	}
	spec.TrustAnchors = anchors

	constraints := spec.Constraints[:0]
	for _, constraint := range spec.Constraints {
		if constraint.Cert == name {
			changed = true
			continue
		}
		constraints = append(constraints, constraint)
	}
	spec.Constraints = constraints

	if !changed {
		return api.TLSPolicySpec{}, false
	}
	return normalizeTLSPolicySpec(spec), true
}

func tlsPolicyMentionsCert(spec api.TLSPolicySpec, name string) bool {
	for _, anchor := range spec.TrustAnchors {
		if anchor.Cert == name {
			return true
		}
	}
	for _, constraint := range spec.Constraints {
		if constraint.Cert == name {
			return true
		}
	}
	return false
}

func makeTLSCertPolicyNotifications(
	m *bboltKVManager,
	changedPolicies map[string]struct{},
	changes *[]tlsChange,
) []tlsPolicyNotification {
	if len(changedPolicies) == 0 {
		return nil
	}
	names := make([]string, 0, len(changedPolicies))
	for name := range changedPolicies {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]tlsPolicyNotification, 0, len(names))
	for _, name := range names {
		*changes = append(*changes, tlsChange{kind: tlsChangePolicyUpsert, name: name})
		out = append(out, m.tlsPolicyNotification(name, name))
	}
	return out
}

func diffTLSOSCerts(oldCerts, newCerts map[string]immutableTLSCert) (deleted, upserted []string) {
	for name := range oldCerts {
		if _, ok := newCerts[name]; !ok {
			deleted = append(deleted, name)
		}
	}
	for name, cert := range newCerts {
		old, ok := oldCerts[name]
		if !ok || old.PEM != cert.PEM {
			upserted = append(upserted, name)
		}
	}
	slices.Sort(deleted)
	slices.Sort(upserted)
	return deleted, upserted
}

func tlsPoliciesAffectedByCerts(
	oldPolicies map[string]tlsEffectivePolicy,
	newPolicies map[string]tlsEffectivePolicy,
	certNames map[string]struct{},
) map[string]struct{} {
	out := map[string]struct{}{}
	for name, policy := range oldPolicies {
		if tlsPolicyMentionsAnyCert(policy.Spec, certNames) {
			out[name] = struct{}{}
		}
	}
	for name, policy := range newPolicies {
		if tlsPolicyMentionsAnyCert(policy.Spec, certNames) {
			out[name] = struct{}{}
		}
	}
	if oldDefault, oldOK := oldPolicies[tlsDefaultPolicyName]; oldOK {
		if newDefault, newOK := newPolicies[tlsDefaultPolicyName]; newOK &&
			!reflect.DeepEqual(oldDefault.Spec, newDefault.Spec) {
			out[tlsDefaultPolicyName] = struct{}{}
		}
	}
	return out
}

func tlsPolicyMentionsAnyCert(spec api.TLSPolicySpec, names map[string]struct{}) bool {
	if len(names) == 0 {
		return false
	}
	for _, anchor := range spec.TrustAnchors {
		if _, ok := names[anchor.Cert]; ok {
			return true
		}
	}
	for _, constraint := range spec.Constraints {
		if _, ok := names[constraint.Cert]; ok {
			return true
		}
	}
	return false
}

func updateMutableTLSPoliciesInTx(
	tx *bbolt.Tx,
	update func(api.TLSPolicySpec) (api.TLSPolicySpec, bool),
	changed map[string]struct{},
) error {
	bucket, err := mutableTLSPoliciesBucket(tx, false)
	if err != nil || bucket == nil {
		return err
	}
	rewrites := map[string][]byte{}
	if err := bucket.ForEach(func(k, v []byte) error {
		record, err := decodeTLSPolicyRecord(v)
		if err != nil {
			return err
		}
		spec, ok := update(record.Spec)
		if !ok {
			return nil
		}
		raw, err := encodeTLSPolicyRecord(tlsPolicyRecord{Spec: spec})
		if err != nil {
			return err
		}
		name := string(k)
		rewrites[name] = raw
		changed[name] = struct{}{}
		return nil
	}); err != nil {
		return err
	}
	for name, raw := range rewrites {
		if err := bucket.Put([]byte(name), raw); err != nil {
			return err
		}
	}
	return nil
}

func (m *bboltKVManager) updateImmutableTLSPolicies(
	update func(api.TLSPolicySpec) (api.TLSPolicySpec, bool),
	changed map[string]struct{},
) {
	m.tlsMu.Lock()
	defer m.tlsMu.Unlock()
	for name, policy := range m.immutableTLS.Policies {
		spec, ok := update(policy.Spec)
		if !ok {
			continue
		}
		policy.Spec = spec
		m.immutableTLS.Policies[name] = policy
		changed[name] = struct{}{}
	}
}

func (m *bboltKVManager) trackTLSPolicyWatcher(policy, node string) {
	m.tlsMu.Lock()
	defer m.tlsMu.Unlock()
	if m.tlsWatchers == nil {
		m.tlsWatchers = map[string]map[string]struct{}{}
	}
	nodes := m.tlsWatchers[policy]
	if nodes == nil {
		nodes = map[string]struct{}{}
		m.tlsWatchers[policy] = nodes
	}
	nodes[node] = struct{}{}
}

func (m *bboltKVManager) tlsPolicyWatchers(policy string) []string {
	m.tlsMu.Lock()
	defer m.tlsMu.Unlock()
	return tlsPolicyWatchersLocked(m.tlsWatchers[policy])
}

func (m *bboltKVManager) tlsPolicyNotification(prevName, newName string) tlsPolicyNotification {
	return tlsPolicyNotification{
		prevName: prevName,
		newName:  newName,
		nodes:    m.tlsPolicyWatchers(prevName),
	}
}

func (m *bboltKVManager) renameTLSPolicyWatchers(name, newName string) []string {
	m.tlsMu.Lock()
	defer m.tlsMu.Unlock()
	nodes := tlsPolicyWatchersLocked(m.tlsWatchers[name])
	if name == newName {
		return nodes
	}
	if m.tlsWatchers == nil {
		return nodes
	}
	oldSet := m.tlsWatchers[name]
	delete(m.tlsWatchers, name)
	if len(oldSet) == 0 {
		return nodes
	}
	newSet := m.tlsWatchers[newName]
	if newSet == nil {
		newSet = map[string]struct{}{}
		m.tlsWatchers[newName] = newSet
	}
	for node := range oldSet {
		newSet[node] = struct{}{}
	}
	return nodes
}

func (m *bboltKVManager) deleteTLSPolicyWatchers(name string) []string {
	m.tlsMu.Lock()
	defer m.tlsMu.Unlock()
	nodes := tlsPolicyWatchersLocked(m.tlsWatchers[name])
	delete(m.tlsWatchers, name)
	return nodes
}

func (m *bboltKVManager) renameTLSPolicyNodeReferences(oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	if !validSecretNodeName(oldName) || !validSecretNodeName(newName) {
		return almagest.ErrTlsConfigInvalidName
	}
	m.tlsMu.Lock()
	defer m.tlsMu.Unlock()
	for policy, nodes := range m.tlsWatchers {
		if _, ok := nodes[oldName]; !ok {
			continue
		}
		delete(nodes, oldName)
		nodes[newName] = struct{}{}
		m.tlsWatchers[policy] = nodes
	}
	return nil
}

func (m *bboltKVManager) deleteTLSPolicyNodeReferences(name string) error {
	if !validSecretNodeName(name) {
		return almagest.ErrTlsConfigInvalidName
	}
	m.tlsMu.Lock()
	defer m.tlsMu.Unlock()
	for policy, nodes := range m.tlsWatchers {
		delete(nodes, name)
		if len(nodes) == 0 {
			delete(m.tlsWatchers, policy)
		}
	}
	return nil
}

func tlsPolicyWatchersLocked(nodes map[string]struct{}) []string {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]string, 0, len(nodes))
	for node := range nodes {
		out = append(out, node)
	}
	slices.Sort(out)
	return out
}

func (m *bboltKVManager) subscribeTLSChanges(callback func(tlsChange)) uint64 {
	m.tlsMu.Lock()
	defer m.tlsMu.Unlock()
	m.tlsNextSub++
	id := m.tlsNextSub
	if m.tlsChangeSubs == nil {
		m.tlsChangeSubs = map[uint64]func(tlsChange){}
	}
	m.tlsChangeSubs[id] = callback
	return id
}

func (m *bboltKVManager) unsubscribeTLSChanges(id uint64) {
	m.tlsMu.Lock()
	defer m.tlsMu.Unlock()
	delete(m.tlsChangeSubs, id)
}

func (m *bboltKVManager) notifyTLSChange(changes ...tlsChange) {
	if len(changes) == 0 {
		return
	}
	m.tlsMu.Lock()
	callbacks := make([]func(tlsChange), 0, len(m.tlsChangeSubs))
	for _, callback := range m.tlsChangeSubs {
		callbacks = append(callbacks, callback)
	}
	m.tlsMu.Unlock()
	for _, change := range changes {
		for _, callback := range callbacks {
			callback(change)
		}
	}
}

func (m *bboltKVManager) immutableTLSCert(name string) (immutableTLSCert, bool) {
	m.tlsMu.Lock()
	defer m.tlsMu.Unlock()
	record, ok := m.immutableTLS.Certs[name]
	return record, ok
}

func (m *bboltKVManager) immutableTLSPolicy(name string) (immutableTLSPolicy, bool) {
	m.tlsMu.Lock()
	defer m.tlsMu.Unlock()
	record, ok := m.immutableTLS.Policies[name]
	if ok {
		record.Spec = normalizeTLSPolicySpec(record.Spec)
	}
	return record, ok
}

func (m *bboltKVManager) cloneImmutableTLSCerts() map[string]immutableTLSCert {
	m.tlsMu.Lock()
	defer m.tlsMu.Unlock()
	out := make(map[string]immutableTLSCert, len(m.immutableTLS.Certs))
	for name, record := range m.immutableTLS.Certs {
		out[name] = record
	}
	return out
}

func (m *bboltKVManager) cloneImmutableTLSPolicies() map[string]immutableTLSPolicy {
	m.tlsMu.Lock()
	defer m.tlsMu.Unlock()
	out := make(map[string]immutableTLSPolicy, len(m.immutableTLS.Policies))
	for name, record := range m.immutableTLS.Policies {
		record.Spec = normalizeTLSPolicySpec(record.Spec)
		out[name] = record
	}
	return out
}

func cloneImmutableTLSData(in immutableTLSData) immutableTLSData {
	out := immutableTLSData{}
	if len(in.Certs) > 0 {
		out.Certs = make(map[string]immutableTLSCert, len(in.Certs))
		for name, record := range in.Certs {
			out.Certs[name] = record
		}
	}
	if len(in.Policies) > 0 {
		out.Policies = make(map[string]immutableTLSPolicy, len(in.Policies))
		for name, record := range in.Policies {
			record.Spec = normalizeTLSPolicySpec(record.Spec)
			out.Policies[name] = record
		}
	}
	return ensureDefaultTLSPolicy(out)
}

func cloneImmutableTLSDataWithoutDefault(in immutableTLSData) immutableTLSData {
	out := immutableTLSData{}
	if len(in.Certs) > 0 {
		out.Certs = make(map[string]immutableTLSCert, len(in.Certs))
		for name, record := range in.Certs {
			out.Certs[name] = record
		}
	}
	if len(in.Policies) > 0 {
		out.Policies = make(map[string]immutableTLSPolicy, len(in.Policies))
		for name, record := range in.Policies {
			if name == tlsDefaultPolicyName && record.source == api.TLSSourceOS {
				continue
			}
			record.Spec = normalizeTLSPolicySpec(record.Spec)
			out.Policies[name] = record
		}
	}
	return out
}

func ensureDefaultTLSPolicy(in immutableTLSData) immutableTLSData {
	if in.Policies == nil {
		in.Policies = map[string]immutableTLSPolicy{}
	}
	in.Policies[tlsDefaultPolicyName] = immutableTLSPolicy{
		tlsPolicyRecord: tlsPolicyRecord{Spec: defaultTLSPolicySpec(in.Certs)},
		source:          api.TLSSourceOS,
	}
	return in
}

func defaultTLSPolicySpec(certs map[string]immutableTLSCert) api.TLSPolicySpec {
	type defaultAnchor struct {
		name  string
		scope api.TLSScope
	}
	anchors := make([]defaultAnchor, 0, len(certs))
	for name, cert := range certs {
		if cert.source != api.TLSSourceOS {
			continue
		}
		_, parsed, err := parseTLSAuthorityPEM(cert.PEM)
		if err != nil {
			continue
		}
		scope, ok := defaultTLSScopeForCertcat(tlsCertcatMatches(parsed))
		if !ok {
			continue
		}
		anchors = append(anchors, defaultAnchor{name: name, scope: scope})
	}
	slices.SortFunc(anchors, func(a, b defaultAnchor) int {
		return strings.Compare(a.name, b.name)
	})
	spec := api.TLSPolicySpec{
		TrustAnchors: make([]api.TLSTrustAnchor, 0, len(anchors)),
	}
	for _, anchor := range anchors {
		spec.TrustAnchors = append(spec.TrustAnchors, api.TLSTrustAnchor{
			Cert:  anchor.name,
			Scope: anchor.scope,
		})
	}
	return spec
}

func encodeTLSCertificateRecord(record tlsCertificateRecord) ([]byte, error) {
	if _, _, err := parseTLSAuthorityPEM(record.PEM); err != nil {
		return nil, fmt.Errorf("%w: %v", almagest.ErrTlsConfigInvalidValue, err)
	}
	return json.Marshal(record)
}

func decodeTLSCertificateRecord(data []byte) (tlsCertificateRecord, error) {
	var record tlsCertificateRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return tlsCertificateRecord{}, fmt.Errorf("%w: decode tls certificate: %v", almagest.ErrTlsConfigInvalidValue, err)
	}
	if _, _, err := parseTLSAuthorityPEM(record.PEM); err != nil {
		return tlsCertificateRecord{}, fmt.Errorf("%w: %v", almagest.ErrTlsConfigInvalidValue, err)
	}
	return record, nil
}

func encodeTLSPolicyRecord(record tlsPolicyRecord) ([]byte, error) {
	record.Spec = normalizeTLSPolicySpec(record.Spec)
	return json.Marshal(record)
}

func decodeTLSPolicyRecord(data []byte) (tlsPolicyRecord, error) {
	var record tlsPolicyRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return tlsPolicyRecord{}, fmt.Errorf("%w: decode tls policy: %v", almagest.ErrTlsConfigInvalidValue, err)
	}
	record.Spec = normalizeTLSPolicySpec(record.Spec)
	return record, nil
}

func mutableTLSCertsBucket(tx *bbolt.Tx, create bool) (*bbolt.Bucket, error) {
	root := tx.Bucket([]byte(tlsRootBucketName))
	if root == nil {
		if !create {
			return nil, nil
		}
		var err error
		root, err = tx.CreateBucket([]byte(tlsRootBucketName))
		if err != nil {
			return nil, err
		}
	}
	if create {
		return root.CreateBucketIfNotExists([]byte(tlsCertsBucketName))
	}
	return root.Bucket([]byte(tlsCertsBucketName)), nil
}

func mutableTLSPoliciesBucket(tx *bbolt.Tx, create bool) (*bbolt.Bucket, error) {
	root := tx.Bucket([]byte(tlsRootBucketName))
	if root == nil {
		if !create {
			return nil, nil
		}
		var err error
		root, err = tx.CreateBucket([]byte(tlsRootBucketName))
		if err != nil {
			return nil, err
		}
	}
	if create {
		return root.CreateBucketIfNotExists([]byte(tlsPoliciesBucketName))
	}
	return root.Bucket([]byte(tlsPoliciesBucketName)), nil
}

func validTLSName(name string) bool {
	return name != "" && utf8.ValidString(name)
}

func validMutableTLSCertName(name string) bool {
	return validTLSName(name) && !strings.HasPrefix(name, tlsOSCertPrefix) && !strings.HasPrefix(name, tlsBuiltinCACertPrefix)
}

func validMutableTLSPolicyName(name string) bool {
	return validTLSName(name) && name != tlsDefaultPolicyName
}

func mapBboltTLSError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, bberrors.ErrDatabaseNotOpen), errors.Is(err, bberrors.ErrTxClosed):
		return almagest.ErrTlsConfigNotFound
	case errors.Is(err, bberrors.ErrBucketNameRequired), errors.Is(err, bberrors.ErrKeyRequired), errors.Is(err, bberrors.ErrKeyTooLarge):
		return almagest.ErrTlsConfigInvalidName
	case errors.Is(err, bberrors.ErrValueTooLarge), errors.Is(err, bberrors.ErrIncompatibleValue):
		return almagest.ErrTlsConfigInvalidValue
	default:
		return err
	}
}

var (
	errTLSCertNotFound   = errors.New("tls certificate was not found")
	errTLSPolicyNotFound = errors.New("tls policy was not found")
)
