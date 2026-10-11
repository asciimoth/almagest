package nativebackend

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sysnetwindows "github.com/asciimoth/sysnet-windows"
)

const (
	WindowsDefaultAdapterNamePrefix = "alm"
	WindowsDefaultOperationTimeout  = 30 * time.Second
)

// WindowsFileConfig is the protected, serializable Windows backend input.
// JSON cannot install the native logger implementation.
type WindowsFileConfig struct {
	AdapterNamePrefix string                      `json:"AdapterNamePrefix"`
	StableGUID        string                      `json:"StableGUID"`
	UnderlaySelector  string                      `json:"UnderlaySelector"`
	Features          sysnetwindows.FeatureConfig `json:"Features"`
	OperationTimeout  windowsDuration             `json:"OperationTimeout"`
	Recovery          windowsRecoveryPolicy       `json:"Recovery"`
	Logger            json.RawMessage             `json:"Logger,omitempty"`
}

type windowsDuration time.Duration

func (d *windowsDuration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("must be a Go duration string: %w", err)
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("invalid Go duration %q: %w", value, err)
	}
	if parsed < 0 {
		return fmt.Errorf("duration must not be negative")
	}
	*d = windowsDuration(parsed)
	return nil
}

type windowsRecoveryPolicy sysnetwindows.RecoveryPolicy

func (p *windowsRecoveryPolicy) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("must be a string: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "inspect":
		*p = windowsRecoveryPolicy(sysnetwindows.RecoveryInspect)
	case "owned":
		*p = windowsRecoveryPolicy(sysnetwindows.RecoveryOwned)
	default:
		return fmt.Errorf("unknown recovery policy %q", value)
	}
	return nil
}

type windowsLogger struct{ logf func(string, ...any) }

func (l windowsLogger) Printf(format string, values ...any) {
	if l.logf != nil {
		l.logf(format, values...)
	}
}

func NewWindowsFileConfig() WindowsFileConfig {
	return WindowsFileConfig{
		AdapterNamePrefix: WindowsDefaultAdapterNamePrefix,
		OperationTimeout:  windowsDuration(WindowsDefaultOperationTimeout),
		Recovery:          windowsRecoveryPolicy(sysnetwindows.RecoveryInspect),
	}
}

// ParseWindowsFileConfig decodes the selected System.Windows JSON object. It
// does not create a backend, load a driver, or perform native recovery.
func ParseWindowsFileConfig(data []byte) (WindowsFileConfig, error) {
	config := NewWindowsFileConfig()
	if err := json.Unmarshal(data, &config); err != nil {
		return WindowsFileConfig{}, fmt.Errorf("system windows config: %w", err)
	}
	return config, nil
}

func (c WindowsFileConfig) Native(logf func(string, ...any)) sysnetwindows.SystemConfig {
	return sysnetwindows.SystemConfig{
		AdapterNamePrefix: c.AdapterNamePrefix,
		StableGUID:        c.StableGUID,
		UnderlaySelector:  c.UnderlaySelector,
		Features:          c.Features,
		OperationTimeout:  time.Duration(c.OperationTimeout),
		Recovery:          sysnetwindows.RecoveryPolicy(c.Recovery),
		Logger:            windowsLogger{logf: logf},
	}
}
