package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
)

func TestFlagsCLIGetAndSet(t *testing.T) {
	for _, test := range []struct {
		name   string
		args   []string
		input  string
		method api.FlagsMethod
	}{
		{name: "get", args: []string{"get"}, method: api.MethodFlagsSnapshot},
		{name: "set", args: []string{"set", "-"}, input: `{"flags":{"sample":true}}`, method: api.MethodFlagsReplace},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := api.NewResponse("1", api.FlagsDocument{Flags: map[string]bool{"sample": true}})
			if err != nil {
				t.Fatal(err)
			}
			transport := &fakeTransport{reads: []api.Message{response}}
			var output bytes.Buffer
			if err := run(context.Background(), append([]string{"flags"}, test.args...), strings.NewReader(test.input), &output, io.Discard,
				func(context.Context, endpoint) (apiTransport, error) { return transport, nil }); err != nil {
				t.Fatal(err)
			}
			if len(transport.writes) != 1 || transport.writes[0].Surface != api.SurfaceFlags || transport.writes[0].Method != string(test.method) {
				t.Fatalf("request = %#v", transport.writes)
			}
			if test.method == api.MethodFlagsReplace {
				var document api.FlagsDocument
				if err := json.Unmarshal(transport.writes[0].Payload, &document); err != nil || !document.Flags["sample"] {
					t.Fatalf("set payload = %#v, %v", document, err)
				}
			}
			if !strings.Contains(output.String(), `"sample": true`) {
				t.Fatalf("output = %q", output.String())
			}
		})
	}
}

func TestFlagsCLIRejectsInvalidDocument(t *testing.T) {
	transport := &fakeTransport{}
	if err := run(context.Background(), []string{"flags", "set"}, strings.NewReader(`{"unknown":true}`), io.Discard, io.Discard,
		func(context.Context, endpoint) (apiTransport, error) { return transport, nil }); err == nil {
		t.Fatal("invalid document was accepted")
	}
	if len(transport.writes) != 0 {
		t.Fatal("invalid document was sent")
	}
}
