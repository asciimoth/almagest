//go:build !windows

package main

// Qualified Linux packages use isolation by default. Other unsupported targets
// keep the fail-closed isolation selection until their policy is defined.
const isolationDefault = true
