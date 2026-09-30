package platform

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/asciimoth/configer/configer"
	sysnetwindows "github.com/asciimoth/sysnet-windows"
	"github.com/kataras/golog"
)

const (
	windowsDefaultAdapterNamePrefix = "alm"
	windowsDefaultOperationTimeout  = 30 * time.Second
)

// windowsSystemConfigFile is the protected, serializable form of the Windows
// backend configuration. Logger is only a raw sink so user input is never
// deserialized into the backend logger interface. The daemon always replaces
// it after it reads the protected config.
type windowsSystemConfigFile struct {
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

type windowsLogger struct{}

func (windowsLogger) Printf(format string, args ...any) {
	golog.Debugf(format, args...)
}

func readWindowsSystemConfig(cfg configer.Config) (sysnetwindows.SystemConfig, error) {
	fileConfig := windowsSystemConfigFile{
		AdapterNamePrefix: windowsDefaultAdapterNamePrefix,
		OperationTimeout:  windowsDuration(windowsDefaultOperationTimeout),
		Recovery:          windowsRecoveryPolicy(sysnetwindows.RecoveryInspect),
	}
	if cfg != nil {
		view := cfg.View(configer.Path{"Windows"})
		if err := configer.Unmarshal(view, &fileConfig); err != nil {
			return sysnetwindows.SystemConfig{}, fmt.Errorf("system windows config: %w", err)
		}
	}
	return sysnetwindows.SystemConfig{
		AdapterNamePrefix: fileConfig.AdapterNamePrefix,
		StableGUID:        fileConfig.StableGUID,
		UnderlaySelector:  fileConfig.UnderlaySelector,
		Features:          fileConfig.Features,
		OperationTimeout:  time.Duration(fileConfig.OperationTimeout),
		Recovery:          sysnetwindows.RecoveryPolicy(fileConfig.Recovery),
		Logger:            windowsLogger{},
	}, nil
}
