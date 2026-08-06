package almagest

import (
	"crypto/tls"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	// DefaultTlsConfigName is the engine-wide TLS client policy used when a
	// node does not select a policy or the selected policy is not available.
	DefaultTlsConfigName = "default"

	nodeTLSConfigFieldID = "tlsConfig"
)

// nodeTLSConfigResult is the resolved TLS client config plus enough metadata
// for a node to handle later TlsConfigChanged inbox messages.
type nodeTLSConfigResult struct {
	config          *tls.Config
	effectiveName   string
	missingSelected bool
}

// readNodeTLSConfigName reads the common node-owned TLS policy selection. An
// empty value means "use the default TLS config".
func readNodeTLSConfigName(cfg configer.Config) string {
	if cfg == nil {
		return ""
	}
	return normalizeNodeTLSConfigName(configer.String(cfg, configer.Path{nodeTLSConfigFieldID}, ""))
}

// nodeTLSConfigValueFromAny reads a menu value for the common TLS config field.
func nodeTLSConfigValueFromAny(raw any) (string, bool) {
	if value, ok := formular.StringValue(raw); ok {
		return normalizeNodeTLSConfigName(value), true
	}
	if value, ok := std.StringFromPayload(raw); ok {
		return normalizeNodeTLSConfigName(value), true
	}
	return "", false
}

func normalizeNodeTLSConfigName(value string) string {
	return strings.TrimSpace(value)
}

// resolveNodeTLSClientConfig gets the selected TLS client config from engine
// storage. Missing and syntactically invalid selections fall back to default.
func resolveNodeTLSClientConfig(base *NodeBase, selectedName string) (nodeTLSConfigResult, error) {
	selectedName = normalizeNodeTLSConfigName(selectedName)
	name := selectedName
	if name == "" {
		name = DefaultTlsConfigName
	}

	storage := nodeTLSConfigStorage(base)
	if storage == nil {
		return nodeTLSConfigResult{
			config:          (&tls.Config{}).Clone(),
			effectiveName:   DefaultTlsConfigName,
			missingSelected: selectedName != "",
		}, nil
	}

	nodeName := currentNodeName(base)
	cfg, err := storage.GetClientConfig(name, nodeName)
	if err == nil {
		return nodeTLSConfigResult{
			config:        (&cfg).Clone(),
			effectiveName: name,
		}, nil
	}
	if name == DefaultTlsConfigName || !tlsConfigMissingErr(err) {
		return nodeTLSConfigResult{effectiveName: name}, err
	}

	cfg, fallbackErr := storage.GetClientConfig(DefaultTlsConfigName, nodeName)
	if fallbackErr != nil {
		return nodeTLSConfigResult{effectiveName: DefaultTlsConfigName}, fmt.Errorf(
			"TLS config %q is not available and default TLS config failed: %w",
			selectedName,
			fallbackErr,
		)
	}
	return nodeTLSConfigResult{
		config:          (&cfg).Clone(),
		effectiveName:   DefaultTlsConfigName,
		missingSelected: true,
	}, nil
}

func cloneTLSClientConfig(cfg *tls.Config) *tls.Config {
	if cfg == nil {
		return nil
	}
	return cfg.Clone()
}

func tlsConfigMissingErr(err error) bool {
	return errors.Is(err, ErrTlsConfigNotFound) || errors.Is(err, ErrTlsConfigInvalidName)
}

func errNodeTLSConfig(errText string) error {
	return fmt.Errorf("TLS config: %s", errText)
}

func nodeTLSConfigStorage(base *NodeBase) TlsConfigStorage {
	if base == nil || base.engine == nil {
		return nil
	}
	return base.engine.TlsConfigStorage()
}

func currentNodeName(base *NodeBase) string {
	if base == nil || base.w == nil || base.id == 0 {
		return ""
	}
	snapshot, ok := base.w.NodeSnapshotLocked(base.id)
	if !ok {
		return ""
	}
	return snapshot.Name
}

// nodeTLSConfigSelectorItem builds the shared TLS policy selector. It always
// includes default, even when a storage implementation fails to list it.
func nodeTLSConfigSelectorItem(base *NodeBase, selectedName, errText string) formular.Item {
	selectedName = normalizeNodeTLSConfigName(selectedName)
	names := nodeTLSConfigMenuNames(nodeTLSConfigStorage(base))
	value := selectedName
	if value == "" || !slices.Contains(names, value) {
		value = DefaultTlsConfigName
	}
	opts := []formular.FieldOption{formular.AllowedValues(stringsToAny(names)...)}
	if errText != "" {
		opts = append(opts, formular.Status(formular.StatusError), formular.StatusText(errText))
	} else if selectedName != "" && !slices.Contains(names, selectedName) {
		opts = append(
			opts,
			formular.Status(formular.StatusWarn),
			formular.StatusText(fmt.Sprintf("selected TLS config %q is not available; using %q", selectedName, DefaultTlsConfigName)),
		)
	}
	return formular.TextField(nodeTLSConfigFieldID, "TLS config", value, opts...)
}

func nodeTLSConfigMenuNames(storage TlsConfigStorage) []string {
	var names []string
	if storage != nil {
		names = storage.ListConfigs()
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(names)+1)
	for _, name := range names {
		name = normalizeNodeTLSConfigName(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if _, ok := seen[DefaultTlsConfigName]; !ok {
		out = append(out, DefaultTlsConfigName)
	}
	slices.Sort(out)
	if idx := slices.Index(out, DefaultTlsConfigName); idx > 0 {
		copy(out[1:idx+1], out[:idx])
		out[0] = DefaultTlsConfigName
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

// handleNodeTLSConfigChanged returns an updated selected name and whether the
// node must reload its resolved TLS config.
func handleNodeTLSConfigChanged(selectedName, effectiveName string, msg TlsConfigChanged) (string, bool) {
	selectedName = normalizeNodeTLSConfigName(selectedName)
	effectiveName = normalizeNodeTLSConfigName(effectiveName)
	prevName := normalizeNodeTLSConfigName(msg.NamePrev)
	newName := normalizeNodeTLSConfigName(msg.NameNew)
	if prevName == "" {
		return selectedName, false
	}

	selectedOrDefault := selectedName
	if selectedOrDefault == "" {
		selectedOrDefault = DefaultTlsConfigName
	}
	if selectedOrDefault == prevName && newName != "" && newName != prevName {
		return newName, true
	}
	if selectedOrDefault == prevName || selectedOrDefault == newName {
		return selectedName, true
	}
	if effectiveName != "" && (effectiveName == prevName || effectiveName == newName) {
		return selectedName, true
	}
	return selectedName, false
}
