package platform

import (
	"fmt"

	"github.com/asciimoth/almagest/bin/internal/nativebackend"
	"github.com/asciimoth/configer/configer"
	sysnetwindows "github.com/asciimoth/sysnet-windows"
	"github.com/kataras/golog"
)

const (
	windowsDefaultAdapterNamePrefix = nativebackend.WindowsDefaultAdapterNamePrefix
	windowsDefaultOperationTimeout  = nativebackend.WindowsDefaultOperationTimeout
)

func readWindowsSystemConfig(cfg configer.Config) (sysnetwindows.SystemConfig, error) {
	fileConfig := nativebackend.NewWindowsFileConfig()
	if cfg != nil {
		view := cfg.View(configer.Path{"Windows"})
		if err := configer.Unmarshal(view, &fileConfig); err != nil {
			return sysnetwindows.SystemConfig{}, fmt.Errorf("system windows config: %w", err)
		}
	}
	return fileConfig.Native(golog.Debugf), nil
}
