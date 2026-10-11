//go:build linux

package network

import (
	"os"
	"testing"

	linuxsysnet "github.com/asciimoth/sysnet-linux"
)

func TestLinuxAllocationParity(t *testing.T) {
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Skip("native allocation checks require Docker")
	}
	s, err := linuxsysnet.New(linuxsysnet.SystemConfig{Features: linuxsysnet.FeatureConfig{MatcherRules: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	nativeAllocationParity(t, s)
}

func TestLinuxSystemCapabilityParity(t *testing.T) {
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Skip("native capability checks require Docker")
	}
	s, err := linuxsysnet.New(linuxsysnet.SystemConfig{Features: linuxsysnet.FeatureConfig{MatcherRules: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	nativeSystemCapabilityParity(t, s)
}
