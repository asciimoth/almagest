package dnstest

import (
	"fmt"
	"testing"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/formular"
)

// DNSClientServerValues builds form values for the DNS client server list.
func DNSClientServerValues(servers ...string) []formular.ArrayElementValue {
	out := make([]formular.ArrayElementValue, 0, len(servers))
	for i, server := range servers {
		out = append(out, formular.ArrayElementValue{
			ID:       fmt.Sprintf("server-%d", i),
			Template: "server",
			Values:   map[string]any{dnsnodes.DNSClientServerField: server},
		})
	}
	return out
}

// DNSClientServerElementValues returns DNS client server field values.
func DNSClientServerElementValues(t *testing.T, elements []formular.ArrayElement) []string {
	t.Helper()
	out := make([]string, 0, len(elements))
	for _, element := range elements {
		var found bool
		for _, item := range element.Items {
			if item.ID != dnsnodes.DNSClientServerField || item.Field == nil {
				continue
			}
			value, ok := formular.StringValue(item.Value)
			if !ok {
				t.Fatalf("server field value = %#v, want string", item.Value)
			}
			out = append(out, value)
			found = true
			break
		}
		if !found {
			t.Fatalf("server element %#v missing URL field", element)
		}
	}
	return out
}
