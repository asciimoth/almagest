package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/asciimoth/almagest/bin/api"
)

func runFlags(ctx context.Context, c *client, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printFlagsHelp(stdout)
		return nil
	}
	switch args[0] {
	case "get":
		return parseNoArgsAndRequest(ctx, c, args[1:], api.SurfaceFlags, string(api.MethodFlagsSnapshot), nil, stdout, stderr)
	case "set":
		fs := commandFlagSet("flags set", stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() > 1 {
			return errors.New("flags set accepts at most one file")
		}
		path := "-"
		if fs.NArg() == 1 {
			path = fs.Arg(0)
		}
		data, err := readConfigInput(path, stdin)
		if err != nil {
			return err
		}
		if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
			return errors.New("flags input must be a JSON object")
		}
		var document api.FlagsDocument
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&document); err != nil {
			return fmt.Errorf("parse flags: %w", err)
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			return errors.New("flags input must contain one JSON object")
		}
		return requestPretty(ctx, c, api.SurfaceFlags, string(api.MethodFlagsReplace), document, stdout)
	default:
		return fmt.Errorf("unknown flags command %q", args[0])
	}
}

func printFlagsHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  almagest-cli flags get
  almagest-cli flags set [file|-]

The file is JSON such as {"flags":{"example":true}}. A missing file reads as {}.
`)
}
