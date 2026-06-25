//go:build !unix

package main

func defaultAdminAPIConfig() AdminAPIConfig {
	return AdminAPIConfig{}
}
