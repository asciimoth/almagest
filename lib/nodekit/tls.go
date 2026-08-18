package nodekit

import (
	"crypto/tls"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/storage"
)

const (
	// DefaultTLSConfigName is the default engine-wide TLS client policy.
	DefaultTLSConfigName = "default"
	// TLSConfigFieldID is the common node-owned TLS policy field.
	TLSConfigFieldID = "tlsConfig"
)

// TLSConfigResult contains a resolved client config and its effective name.
type TLSConfigResult struct {
	Config          *tls.Config
	EffectiveName   string
	MissingSelected bool
}

// ReadTLSConfigName reads the common node-owned TLS policy selection.
func ReadTLSConfigName(cfg configer.Config) string {
	if cfg == nil {
		return ""
	}
	return NormalizeTLSConfigName(configer.String(cfg, configer.Path{TLSConfigFieldID}, ""))
}

// TLSConfigValueFromAny reads a common TLS config menu value.
func TLSConfigValueFromAny(raw any) (string, bool) {
	if value, ok := formular.StringValue(raw); ok {
		return NormalizeTLSConfigName(value), true
	}
	if value, ok := std.StringFromPayload(raw); ok {
		return NormalizeTLSConfigName(value), true
	}
	return "", false
}

// NormalizeTLSConfigName normalizes a selected TLS config name.
func NormalizeTLSConfigName(value string) string {
	return strings.TrimSpace(value)
}

// ResolveTLSClientConfig resolves a selected client config from node storage.
func ResolveTLSClientConfig(base NodeContext, selectedName string) (TLSConfigResult, error) {
	selectedName = NormalizeTLSConfigName(selectedName)
	name := selectedName
	if name == "" {
		name = DefaultTLSConfigName
	}

	store := tlsConfigStorage(base)
	if store == nil {
		return TLSConfigResult{
			Config:          (&tls.Config{}).Clone(),
			EffectiveName:   DefaultTLSConfigName,
			MissingSelected: selectedName != "",
		}, nil
	}

	cfg, err := store.GetClientConfig(name, currentNodeName(base))
	if err == nil {
		return TLSConfigResult{Config: (&cfg).Clone(), EffectiveName: name}, nil
	}
	if name == DefaultTLSConfigName || !tlsConfigMissingErr(err) {
		return TLSConfigResult{EffectiveName: name}, err
	}

	cfg, fallbackErr := store.GetClientConfig(DefaultTLSConfigName, currentNodeName(base))
	if fallbackErr != nil {
		return TLSConfigResult{EffectiveName: DefaultTLSConfigName}, fmt.Errorf(
			"TLS config %q is not available and default TLS config failed: %w",
			selectedName,
			fallbackErr,
		)
	}
	return TLSConfigResult{
		Config:          (&cfg).Clone(),
		EffectiveName:   DefaultTLSConfigName,
		MissingSelected: true,
	}, nil
}

// ResolveTLSLocalCA resolves the engine-local TLS CA from node storage.
func ResolveTLSLocalCA(base NodeContext) (tls.Certificate, error) {
	store := tlsConfigStorage(base)
	if store == nil {
		return tls.Certificate{}, storage.ErrTlsConfigNotFound
	}
	ca, err := store.GetLocalCA(currentNodeName(base))
	if err != nil {
		return tls.Certificate{}, err
	}
	return CloneTLSCertificate(ca), nil
}

// CloneTLSClientConfig returns an independent TLS config copy.
func CloneTLSClientConfig(cfg *tls.Config) *tls.Config {
	if cfg == nil {
		return nil
	}
	return cfg.Clone()
}

// CloneTLSCertificate returns an independent TLS certificate value. PrivateKey
// is copied by reference because key implementations are not generally
// cloneable.
func CloneTLSCertificate(cert tls.Certificate) tls.Certificate {
	out := tls.Certificate{
		Certificate:                  cloneTLSByteSlices(cert.Certificate),
		PrivateKey:                   cert.PrivateKey,
		SupportedSignatureAlgorithms: append([]tls.SignatureScheme(nil), cert.SupportedSignatureAlgorithms...),
		OCSPStaple:                   append([]byte(nil), cert.OCSPStaple...),
		SignedCertificateTimestamps:  cloneTLSByteSlices(cert.SignedCertificateTimestamps),
		Leaf:                         cert.Leaf,
	}
	return out
}

// TLSConfigSelectorItem builds the common TLS policy selector.
func TLSConfigSelectorItem(base NodeContext, selectedName, errText string) formular.Item {
	selectedName = NormalizeTLSConfigName(selectedName)
	names := tlsConfigMenuNames(tlsConfigStorage(base))
	value := selectedName
	if value == "" || !slices.Contains(names, value) {
		value = DefaultTLSConfigName
	}
	opts := []formular.FieldOption{formular.AllowedValues(stringsToAny(names)...)}
	if errText != "" {
		opts = append(opts, formular.Status(formular.StatusError), formular.StatusText(errText))
	} else if selectedName != "" && !slices.Contains(names, selectedName) {
		opts = append(
			opts,
			formular.Status(formular.StatusWarn),
			formular.StatusText(fmt.Sprintf(
				"selected TLS config %q is not available; using %q",
				selectedName,
				DefaultTLSConfigName,
			)),
		)
	}
	return formular.TextField(TLSConfigFieldID, "TLS config", value, opts...)
}

// HandleTLSConfigChanged updates a selection after a storage notification.
func HandleTLSConfigChanged(selectedName, effectiveName string, msg storage.TlsConfigChanged) (string, bool) {
	selectedName = NormalizeTLSConfigName(selectedName)
	effectiveName = NormalizeTLSConfigName(effectiveName)
	prevName := NormalizeTLSConfigName(msg.NamePrev)
	newName := NormalizeTLSConfigName(msg.NameNew)

	selectedOrDefault := selectedName
	if selectedOrDefault == "" {
		selectedOrDefault = DefaultTLSConfigName
	}
	if prevName != selectedOrDefault && newName != selectedOrDefault && prevName != effectiveName && newName != effectiveName {
		return selectedName, false
	}
	if selectedName != "" && prevName == selectedName && newName != "" {
		selectedName = newName
	}
	return selectedName, true
}

func tlsConfigStorage(base NodeContext) storage.TlsConfigStorage {
	if base == nil || base.NodeRuntime() == nil {
		return nil
	}
	return base.NodeRuntime().TlsConfigStorage()
}

func currentNodeName(base NodeContext) string {
	if base == nil || base.NodeWorkspace() == nil || base.NodeID() == 0 {
		return ""
	}
	snapshot, ok := base.NodeWorkspace().NodeSnapshotLocked(base.NodeID())
	if !ok {
		return ""
	}
	return snapshot.Name
}

func tlsConfigMissingErr(err error) bool {
	return errors.Is(err, storage.ErrTlsConfigNotFound) || errors.Is(err, storage.ErrTlsConfigInvalidName)
}

func tlsConfigMenuNames(store storage.TlsConfigStorage) []string {
	var names []string
	if store != nil {
		names = store.ListConfigs()
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(names)+1)
	for _, name := range names {
		name = NormalizeTLSConfigName(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if _, ok := seen[DefaultTLSConfigName]; !ok {
		out = append(out, DefaultTLSConfigName)
	}
	slices.Sort(out)
	if idx := slices.Index(out, DefaultTLSConfigName); idx > 0 {
		copy(out[1:idx+1], out[:idx])
		out[0] = DefaultTLSConfigName
	}
	return out
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
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
