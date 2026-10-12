package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: command-recorder OUTPUT VALUE...")
		os.Exit(2)
	}
	if delay := os.Getenv("E2E_CALLBACK_BEFORE_RECORD_SLEEP"); delay != "" {
		duration, err := time.ParseDuration(delay + "s")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		time.Sleep(duration)
	}
	if secret := os.Getenv("E2E_CALLBACK_STDOUT_SECRET"); secret != "" {
		fmt.Fprintln(os.Stdout, secret)
		fmt.Fprintln(os.Stderr, secret)
	}
	output, err := os.OpenFile(os.Args[1], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer output.Close() //nolint:errcheck // Process exit closes the test fixture output.
	line := strings.Join(os.Args[2:], "|")
	if environment := os.Getenv("E2E_CALLBACK_ENV"); environment != "" {
		line = fmt.Sprintf("%s|uid=%d|gid=%d|env=%s|argc=%d|%s", os.Args[2], os.Getuid(), os.Getgid(), environment, len(os.Args)-3, strings.Join(os.Args[3:], "|"))
	}
	if _, err := fmt.Fprintln(output, line); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if delay := os.Getenv("E2E_CALLBACK_SLEEP"); delay != "" {
		duration, err := time.ParseDuration(delay + "s")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		time.Sleep(duration)
	}
}
