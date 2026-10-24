//go:build linux

package tlsplatform

import "testing"

func TestLinuxDistrustStatusIsConservativeAndContainsNoSelector(t *testing.T) {
	request, err := ConfiguredRequest(true, "off")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := systemSecuritySnapshot(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Distrust) != 0 || len(snapshot.CRLs) != 0 || len(snapshot.Providers) != 1 || snapshot.Providers[0].Source != linuxDistrustProvider {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if state := snapshot.Providers[0].State; state != ProviderUnsupported && state != ProviderUnavailable {
		t.Fatalf("provider state = %q", state)
	}
	disabled, err := systemSecuritySnapshot(t.Context(), DefaultRequest())
	if err != nil || len(disabled.Providers) != 1 || disabled.Providers[0].State != ProviderDisabled {
		t.Fatalf("disabled snapshot = %#v, %v", disabled, err)
	}
}
