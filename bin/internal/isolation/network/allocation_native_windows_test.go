//go:build windows

package network

import (
	"os"
	"strings"
	"testing"

	windowsnet "github.com/asciimoth/sysnet-windows"
)

func TestWindowsAllocationParity(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || !strings.EqualFold(host, "ALM-WINVM") {
		t.Skip("native allocation checks require ALM-WINVM")
	}
	s, err := windowsnet.New(windowsnet.SystemConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	nativeAllocationParity(t, s)
}

func TestWindowsSystemCapabilityParity(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || !strings.EqualFold(host, "ALM-WINVM") {
		t.Skip("native capability checks require ALM-WINVM")
	}
	s, err := windowsnet.New(windowsnet.SystemConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	nativeSystemCapabilityParity(t, s)
}
