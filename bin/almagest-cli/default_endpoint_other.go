//go:build !unix && !windows

package main

const defaultEndpoint = "unix://run/almagest/admin.sock"
