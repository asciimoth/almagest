package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/typecolor"
	"github.com/asciimoth/configer/configer"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

const webdemoNodeClassCacheVersion = 1

// webdemoNodeClassCache contains only the data used by documentation node
// previews and the demo SPA. Order preserves the daemon's canonical class
// order. The class name is the map key, so it is not repeated in each entry.
// Descriptions and runtime state do not belong in this build cache.
type webdemoNodeClassCache struct {
	Version  int                                `json:"version"`
	Order    []string                           `json:"order"`
	ColorMap map[string]typecolor.TypePalette   `json:"colormap"`
	Classes  map[string]webdemoNodeClassPreview `json:"classes"`
}

type webdemoNodeClassPreview struct {
	PrimaryType  string                        `json:"primary_type"`
	InitialPorts []pasta.NodeClassPortSnapshot `json:"initial_ports"`
}

// writeWebdemoNodeClassCache builds the same stable class set as a normal
// daemon. It uses empty in-memory services because class snapshots only read
// registration metadata and do not start node runtime resources.
func writeWebdemoNodeClassCache(output io.Writer) error {
	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1),
		&sysnetdebug.System{},
		nil,
		nil,
		nil,
		nil,
		daemonNodeClasses(cmdrunner.Config{}, nil),
		nil,
		false,
	)
	if err != nil {
		return fmt.Errorf("build metadata engine: %w", err)
	}
	defer func() { _ = engine.Close() }()

	snapshot, ok := engine.WorkspaceSnapshot(almagest.MainWorkspace)
	if !ok {
		return fmt.Errorf("snapshot metadata workspace")
	}
	classes := api.SortedNodeClassSnapshots(snapshot.Classes, nil)
	cache := webdemoNodeClassCache{
		Version:  webdemoNodeClassCacheVersion,
		Order:    make([]string, 0, len(classes)),
		ColorMap: typecolor.DefaultColorMap,
		Classes:  make(map[string]webdemoNodeClassPreview, len(classes)),
	}
	for _, class := range classes {
		if isDebugDocsClass(class.ShortDescription) {
			continue
		}
		cache.Order = append(cache.Order, class.Class)
		cache.Classes[class.Class] = webdemoNodeClassPreview{
			PrimaryType:  class.PrimaryType,
			InitialPorts: class.InitialPorts,
		}
	}

	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(cache); err != nil {
		return fmt.Errorf("encode web demo node class cache: %w", err)
	}
	return nil
}
