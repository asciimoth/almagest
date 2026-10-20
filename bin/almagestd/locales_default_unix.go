//go:build !windows

package main

import "os"

func defaultLocalesRoot() string {
	if info, err := os.Stat("/locales"); err == nil && info.IsDir() {
		return "/locales"
	}
	return "/usr/share/almagest/locales"
}
