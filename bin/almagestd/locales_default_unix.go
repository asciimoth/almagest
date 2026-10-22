//go:build !windows

package main

import "os"

func defaultLocalesRoot() string {
	if info, err := os.Stat("/locales"); err == nil && info.IsDir() {
		return "/locales"
	}
	return "/usr/share/almagest/locales"
}

func defaultExamplesRoot() string {
	if info, err := os.Stat("/examples"); err == nil && info.IsDir() {
		return "/examples"
	}
	return "/usr/share/almagest/examples"
}
