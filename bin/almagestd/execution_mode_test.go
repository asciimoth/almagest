package main

import (
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/almagestd/args"
)

func TestSelectExecutionMode(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments args.Args
		want      executionMode
		wantError string
	}{
		{name: "direct", arguments: args.Args{NoIsolation: true}, want: executionModeDirect},
		{name: "direct experimental", arguments: args.Args{NoIsolation: true, Experimental: true}, want: executionModeDirect},
		{name: "isolation unavailable", wantError: "isolation is not implemented yet"},
		{
			name:      "experimental needs explicit direct mode",
			arguments: args.Args{Experimental: true},
			wantError: "--experimental requires explicit --no-isolation",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectExecutionMode(test.arguments)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("selectExecutionMode() error = %v, want error containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("selectExecutionMode() = %q, want %q", got, test.want)
			}
		})
	}
}
