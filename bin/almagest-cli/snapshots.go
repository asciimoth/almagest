package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/asciimoth/almagest/bin/api"
)

func runSnapshots(
	ctx context.Context,
	c *client,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printSnapshotsHelp(stdout)
		return nil
	}

	switch args[0] {
	case "create":
		flags := commandFlagSet("snapshots create", stderr)
		name := flags.String("name", "", "optional snapshot name; names do not need to be unique")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("snapshots create does not accept positional arguments")
		}
		return requestPretty(ctx, c, api.SurfaceSnapshots, string(api.MethodSnapshotsCreate), api.SnapshotsCreateRequest{Name: *name}, stdout)

	case "list":
		return parseNoArgsAndRequest(ctx, c, args[1:], api.SurfaceSnapshots, string(api.MethodSnapshotsList), struct{}{}, stdout, stderr)

	case "rename":
		flags := commandFlagSet("snapshots rename", stderr)
		uuidFlag := flags.String("uuid", "", "UUID of the snapshot to rename")
		name := flags.String("name", "", "new snapshot name; use an empty value to remove the name")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		nameSet := false
		flags.Visit(func(flag *flag.Flag) {
			if flag.Name == "name" {
				nameSet = true
			}
		})
		if !nameSet {
			return fmt.Errorf("new snapshot name is required; use --name with an empty value to remove the name")
		}
		uuid, err := resolveSnapshotUUID("rename", *uuidFlag, flags.Args())
		if err != nil {
			return err
		}
		request := api.SnapshotsRenameRequest{UUID: uuid, Name: *name}
		return requestPretty(ctx, c, api.SurfaceSnapshots, string(api.MethodSnapshotsRename), request, stdout)

	case "delete":
		uuid, err := parseSnapshotUUIDArgument("delete", args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceSnapshots, string(api.MethodSnapshotsDelete), api.SnapshotUUIDRequest{UUID: uuid}, stdout)

	case "restore":
		uuid, err := parseSnapshotUUIDArgument("restore", args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceSnapshots, string(api.MethodSnapshotsRestore), api.SnapshotUUIDRequest{UUID: uuid}, stdout)

	default:
		return fmt.Errorf("unknown snapshots command %q", args[0])
	}
}

func parseSnapshotUUIDArgument(command string, args []string, stderr io.Writer) (string, error) {
	flags := commandFlagSet("snapshots "+command, stderr)
	uuid := flags.String("uuid", "", "UUID of the snapshot to "+command)
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	return resolveSnapshotUUID(command, *uuid, flags.Args())
}

func resolveSnapshotUUID(command, uuid string, args []string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("snapshots %s accepts one UUID", command)
	}
	if uuid != "" && len(args) != 0 {
		return "", fmt.Errorf("specify the snapshot UUID with --uuid or as an argument, not both")
	}
	if uuid == "" && len(args) == 1 {
		uuid = args[0]
	}
	if uuid == "" {
		return "", fmt.Errorf("snapshot UUID is required")
	}
	return uuid, nil
}

func printSnapshotsHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  almagest-cli snapshots create [--name NAME]
  almagest-cli snapshots list
  almagest-cli snapshots delete UUID
  almagest-cli snapshots delete --uuid UUID
  almagest-cli snapshots rename --name NAME UUID
  almagest-cli snapshots rename --name NAME --uuid UUID
  almagest-cli snapshots restore UUID
  almagest-cli snapshots restore --uuid UUID

Commands:
  create   save regular config, bbolt data, and daemon file storage
  delete   permanently delete one snapshot
  list     list snapshots in newest-first order
  rename   change or remove a snapshot display name
  restore  restore all saved state and rebuild the engine graph

Snapshots are identified by UUID. Optional names do not need to be unique.
Protected config is never included or replaced.
`)
}
