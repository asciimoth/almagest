package almagest

import (
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/gonnect"
)

func enginePool(e *engine) bufpool.Pool {
	if e == nil {
		return nil
	}
	return e.Pool()
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
