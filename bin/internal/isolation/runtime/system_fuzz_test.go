package runtime

import (
	"net/netip"
	"testing"

	"github.com/asciimoth/gonnect/sysnet"
)

func FuzzSystemTunOptions(f *testing.F) {
	for _, seed := range []string{"", "192.0.2.1", "192.0.2.1/24", "2001:db8::1/128", "host.invalid", "192.0.2.0/33", "\x00"} {
		f.Add(seed, 1400, false)
	}
	f.Fuzz(func(t *testing.T, value string, mtu int, defaultTun bool) {
		if mtu < -1_000_000 || mtu > 1_000_000 {
			return
		}
		if defaultTun {
			request, err := defaultTunRequest(sysnet.DefaultTunOpts{MTU: mtu, TunAddrs: []string{value}, TunRoutes: []string{value}})
			if err == nil {
				for _, prefix := range append(request.Options.Addresses, request.Options.Routes...) {
					if !prefix.IsValid() {
						t.Fatal("successful conversion returned an invalid prefix")
					}
				}
			}
			return
		}
		options, err := tunOptions(sysnet.TunOpts{MTU: mtu, TunAddrs: []string{value}, TunRoutes: []string{value}})
		if err == nil {
			for _, prefix := range append(options.Addresses, options.Routes...) {
				if !prefix.IsValid() {
					t.Fatal("successful conversion returned an invalid prefix")
				}
				if _, parseErr := netip.ParsePrefix(prefix.String()); parseErr != nil {
					t.Fatal(parseErr)
				}
			}
		}
	})
}
