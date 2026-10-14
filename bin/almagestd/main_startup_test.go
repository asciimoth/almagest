package main

import (
	"slices"
	"testing"
)

func TestExecutionModeStartupWarnings(t *testing.T) {
	want := []string{
		"Daemon process isolation is disabled",
		"A compromised graph runtime can access all daemon privileges and host resources",
	}
	if got := executionModeStartupWarnings(executionModeDirect); !slices.Equal(got, want) {
		t.Fatalf("direct mode warnings = %#v, want %#v", got, want)
	}

	for _, mode := range []executionMode{executionModeIsolation, executionModeIsolationDevelopment} {
		if got := executionModeStartupWarnings(mode); len(got) != 0 {
			t.Errorf("%s mode warnings = %#v, want none", mode, got)
		}
	}
}
