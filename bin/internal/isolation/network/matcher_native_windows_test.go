//go:build windows

package network

import (
	"os"
	"strings"
	"testing"

	windowsnet "github.com/asciimoth/sysnet-windows"
)

func TestWindowsMatcherParity(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || !strings.EqualFold(host, "ALM-WINVM") {
		t.Skip("native matcher checks require ALM-WINVM")
	}
	s, err := windowsnet.New(windowsnet.SystemConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	nativeMatcherParity(t, s, "win-pid")
}

func TestWindowsRuleParity(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || !strings.EqualFold(host, "ALM-WINVM") {
		t.Skip("native matcher checks require ALM-WINVM")
	}
	s, err := windowsnet.New(windowsnet.SystemConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	nativeRuleParity(t, s)
}
