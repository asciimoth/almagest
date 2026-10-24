package webdemo

import (
	"encoding/json"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
)

func TestLiveBackendRejectsInvalidMessages(t *testing.T) {
	backend, _ := newTestLiveBackend(t)
	for _, input := range []string{"not-json", `{}`, `{"version":"v1","kind":"event","surface":"admin","method":"ping"}`} {
		response, err := api.UnmarshalMessage(backend.Handle([]byte(input)))
		if err != nil {
			t.Fatalf("response to %q is invalid: %v", input, err)
		}
		if response.Error == nil {
			t.Fatalf("response to %q has no error", input)
		}
	}
	response := liveCall(t, backend, api.SurfaceAdmin, "missing", struct{}{})
	if response.Error == nil || response.Error.Code != api.ErrorInvalidMethod {
		t.Fatalf("unknown method error = %#v", response.Error)
	}
}

func decodeResult(t *testing.T, response api.Message, target any) {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("API error = %#v", response.Error)
	}
	if err := json.Unmarshal(response.Result, target); err != nil {
		t.Fatal(err)
	}
}
