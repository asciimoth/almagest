package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/asciimoth/almagest/bin/api"
)

func runConfig(
	ctx context.Context,
	c *client,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printConfigHelp(stdout)
		return nil
	}

	switch args[0] {
	case "get":
		fs := commandFlagSet("config get", stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("config get does not accept positional arguments")
		}
		raw, err := c.request(ctx, api.SurfaceConfig, string(api.MethodConfigSnapshot), struct{}{})
		if err != nil {
			return err
		}
		var snapshot api.ConfigSnapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return err
		}
		_, err = io.WriteString(stdout, snapshot.Text)
		return err

	case "snapshot":
		fs := commandFlagSet("config snapshot", stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("config snapshot does not accept positional arguments")
		}
		return requestPretty(ctx, c, api.SurfaceConfig, string(api.MethodConfigSnapshot), struct{}{}, stdout)

	case "apply", "update":
		fs := commandFlagSet("config "+args[0], stderr)
		revision := fs.String("revision", "", "expected revision from config snapshot")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() > 1 {
			return fmt.Errorf("config %s accepts at most one file", args[0])
		}
		path := "-"
		if fs.NArg() == 1 {
			path = fs.Arg(0)
		}
		data, err := readConfigInput(path, stdin)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceConfig, string(api.MethodConfigUpdate), api.ConfigUpdateRequest{
			Text:     string(data),
			Revision: *revision,
		}, stdout)

	default:
		return fmt.Errorf("unknown config command %q", args[0])
	}
}

func readConfigInput(path string, stdin io.Reader) ([]byte, error) {
	if path == "-" {
		if stdin == nil {
			return nil, errors.New("config input is not available")
		}
		return io.ReadAll(stdin)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return data, nil
}

func printConfigHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  almagest-cli config get
  almagest-cli config snapshot
  almagest-cli config apply [--revision REVISION] [FILE|-]

Commands:
  get       print only the editable HuJSON document
  snapshot  print the document, revision, and read-only state as JSON
  apply     read HuJSON from FILE or standard input and apply it
  update    alias for apply

The config surface never reads or writes protected daemon config. Use
--revision with the value from "config snapshot" to reject stale edits.
`)
}
