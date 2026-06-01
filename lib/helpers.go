package lib

import (
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
)

func readConfigString(cfg configer.Config, key, fallback string) string {
	if cfg == nil {
		return fallback
	}
	raw, err := cfg.Get(configer.Path{key})
	if err != nil {
		return fallback
	}
	if value, ok := raw.(string); ok {
		return value
	}
	return fallback
}

func readConfigInt(cfg configer.Config, key string, fallback int) int {
	if cfg == nil {
		return fallback
	}
	raw, err := cfg.Get(configer.Path{key})
	if err != nil {
		return fallback
	}
	switch value := raw.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return fallback
	}
}

func parsePort(value string, fallback int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}

	port, err := strconv.Atoi(value)
	if err != nil {
		port, err = gonnect.LookupPortOffline("ip", value)
		if err != nil {
			return fallback
		}
	}

	if port <= 0 || port > 65535 {
		return 0
	}

	return port
}

func sleepOrStopped(stop <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}
