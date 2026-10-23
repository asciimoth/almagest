//go:build wasm

package almagest

import (
	"slices"
	"testing"

	httpnodes "github.com/asciimoth/almagest/lib/http"
	networknodes "github.com/asciimoth/almagest/lib/network"
)

func TestWASMNodeClasses(t *testing.T) {
	want := []string{
		httpnodes.NodeTypeHTTPClient,
		httpnodes.NodeTypeHTTPServer,
		networknodes.NodeTypeMockNetwork,
	}

	for _, debug := range []bool{false, true} {
		classes := nodeClasses(&engine{}, debug)
		got := make([]string, 0, len(classes))
		for _, class := range classes {
			got = append(got, class.ClassName())
		}
		if !slices.Equal(got, want) {
			t.Fatalf("nodeClasses(debug=%t) = %v, want %v", debug, got, want)
		}
	}
}
