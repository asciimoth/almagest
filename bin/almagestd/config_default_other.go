//go:build !unix && !windows

package main

func defaultAdminAPIConfig() AdminAPIConfig {
	return AdminAPIConfig{}
}
