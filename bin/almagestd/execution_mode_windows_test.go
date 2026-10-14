//go:build windows

package main

import (
	"testing"

	"github.com/asciimoth/almagest/bin/almagestd/args"
)

func TestWindowsDefaultExecutionModeIsDirect(t *testing.T) {
	for _, arguments := range []args.Args{
		{},
		{NoIsolation: true},
		{Experimental: true},
	} {
		mode, err := selectExecutionMode(arguments)
		if err != nil {
			t.Fatalf("selectExecutionMode(%+v): %v", arguments, err)
		}
		if mode != executionModeDirect {
			t.Fatalf("selectExecutionMode(%+v) = %q, want %q", arguments, mode, executionModeDirect)
		}
	}
}
