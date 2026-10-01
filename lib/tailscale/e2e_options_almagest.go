//go:build almagest_e2e

package tailscalenodes

import (
	"os"
	"strconv"

	wgotailscale "github.com/asciimoth/wgo-tailscale"
	wgdevice "github.com/asciimoth/wgo/device"
)

const tailscaleE2ERejectAfterEnv = "ALMAGEST_TAILSCALE_E2E_REJECT_AFTER_SECONDS"

// applyTailscaleE2EOptions lets a hermetic E2E case shorten the time that wgo
// keeps a peer connected after its last successful handshake. The generated
// profile keeps the standard WireGuard packet headers and changes only the
// local peer timer. Production builds do not include this override.
func applyTailscaleE2EOptions(options wgotailscale.Options) wgotailscale.Options {
	raw := os.Getenv(tailscaleE2ERejectAfterEnv)
	if raw == "" {
		return options
	}
	seconds, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || seconds == 0 {
		return options
	}
	profile := wgdevice.DefaultAmneziaWGConfig()
	profile.RejectAfterTime = wgdevice.AmneziaWGRange{
		Min: uint32(seconds), Max: uint32(seconds), Set: true,
	}
	options.Obfuscation = &profile
	return options
}
