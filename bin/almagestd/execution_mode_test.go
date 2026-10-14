package main

import (
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/almagestd/args"
)

func TestSelectExecutionMode(t *testing.T) {
	for _, test := range []struct {
		name             string
		isolateByDefault bool
		arguments        args.Args
		want             executionMode
		wantError        string
	}{
		{name: "explicit direct", isolateByDefault: true, arguments: args.Args{NoIsolation: true}, want: executionModeDirect},
		{name: "explicit direct experimental", isolateByDefault: true, arguments: args.Args{NoIsolation: true, Experimental: true}, want: executionModeDirect},
		{name: "isolation default", isolateByDefault: true, want: executionModeIsolation},
		{
			name:             "isolation default experimental needs explicit direct mode",
			isolateByDefault: true,
			arguments:        args.Args{Experimental: true},
			wantError:        "--experimental requires explicit --no-isolation",
		},
		{name: "direct default", want: executionModeDirect},
		{name: "direct default explicit flag", arguments: args.Args{NoIsolation: true}, want: executionModeDirect},
		{name: "direct default experimental", arguments: args.Args{Experimental: true}, want: executionModeDirect},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectExecutionModeForPlatform(test.arguments, test.isolateByDefault)
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
