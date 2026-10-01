//go:build !almagest_e2e

package tailscalenodes

import wgotailscale "github.com/asciimoth/wgo-tailscale"

func applyTailscaleE2EOptions(options wgotailscale.Options) wgotailscale.Options {
	return options
}
