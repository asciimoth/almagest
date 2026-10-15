package main

import (
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/almagestd/args"
	"github.com/asciimoth/almagest/bin/internal/isolation/selection"
)

func TestSelectExecutionMode(t *testing.T) {
	for _, test := range []struct {
		name      string
		platform  selection.Platform
		arguments args.Args
		want      executionMode
		wantError string
	}{
		{name: "explicit direct", platform: selection.Platform{OS: "linux", Arch: "amd64"}, arguments: args.Args{NoIsolation: true}, want: executionModeDirect},
		{name: "explicit direct experimental", platform: selection.Platform{OS: "linux", Arch: "amd64"}, arguments: args.Args{NoIsolation: true, Experimental: true}, want: executionModeDirect},
		{name: "Linux automatic default", platform: selection.Platform{OS: "linux", Arch: "amd64"}, want: executionModeIsolation},
		{
			name:      "isolation default experimental needs explicit direct mode",
			platform:  selection.Platform{OS: "linux", Arch: "amd64"},
			arguments: args.Args{Experimental: true},
			wantError: "--experimental requires explicit --no-isolation or --isolation=off",
		},
		{name: "Windows direct default", platform: selection.Platform{OS: "windows", Arch: "amd64"}, want: executionModeDirect},
		{name: "Windows direct default explicit flag", platform: selection.Platform{OS: "windows", Arch: "amd64"}, arguments: args.Args{NoIsolation: true}, want: executionModeDirect},
		{name: "Windows direct default experimental", platform: selection.Platform{OS: "windows", Arch: "amd64"}, arguments: args.Args{Experimental: true}, want: executionModeDirect},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectExecutionModeForPlatform(test.arguments, test.platform)
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
