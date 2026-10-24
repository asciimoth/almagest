package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/alexflint/go-arg"
)

func TestParseDaemonArgsHelp(t *testing.T) {
	var output bytes.Buffer

	_, _, err := parseDaemonArgs([]string{"--help"}, &output)
	if !errors.Is(err, arg.ErrHelp) {
		t.Fatalf("parseDaemonArgs(--help) error = %v, want arg.ErrHelp", err)
	}
	if help := output.String(); !strings.Contains(help, "Usage:") || !strings.Contains(help, "--help") {
		t.Fatalf("parseDaemonArgs(--help) output does not contain usage and help option:\n%s", help)
	}
}

func TestParseDaemonArgsWebdemoClassDump(t *testing.T) {
	arguments, _, err := parseDaemonArgs([]string{"--dump-webdemo-node-classes"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !arguments.DumpWebdemoClasses {
		t.Fatal("--dump-webdemo-node-classes was not parsed")
	}
}
