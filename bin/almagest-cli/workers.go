package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/pasta/pasta"
)

// runWorkers owns CLI commands for engine worker-system diagnostics.
func runWorkers(ctx context.Context, c *client, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printWorkersHelp(stdout)
		return nil
	}
	switch args[0] {
	case "tree":
		workspace, err := parseWorkspace("workers tree", args[1:], stderr)
		if err != nil {
			return err
		}
		snapshot, err := fetchWorkersSnapshot(ctx, c, workspace)
		if err != nil {
			return err
		}
		_, err = stdout.Write([]byte(renderWorkersTree(workspace, snapshot, time.Now())))
		return err
	case "snapshot":
		workspace, err := parseWorkspace("workers snapshot", args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceEngine, string(api.MethodEngineWorkersSnapshot), api.WorkspaceRequest{Workspace: workspace}, stdout)
	default:
		return fmt.Errorf("unknown workers command %q", args[0])
	}
}

func fetchWorkersSnapshot(ctx context.Context, c *client, workspace string) (api.WorkersSnapshotResponse, error) {
	raw, err := c.request(ctx, api.SurfaceEngine, string(api.MethodEngineWorkersSnapshot), api.WorkspaceRequest{Workspace: workspace})
	if err != nil {
		return api.WorkersSnapshotResponse{}, err
	}
	return api.DecodePayload[api.WorkersSnapshotResponse](raw)
}

// renderWorkersTree prints live worker records under their parent node. The
// parent node may be a live placeholder or a gone node reconstructed from the
// worker's captured node metadata.
func renderWorkersTree(workspace string, snapshot api.WorkersSnapshotResponse, now time.Time) string {
	if workspace == "" {
		workspace = "workspace"
	}

	byNode := map[uint64][]almagest.WorkerSnapshot{}
	for _, worker := range snapshot.Workers {
		byNode[worker.Node] = append(byNode[worker.Node], worker)
	}

	nodeIDs := make([]uint64, 0, len(byNode))
	for node := range byNode {
		nodeIDs = append(nodeIDs, node)
		sort.Slice(byNode[node], func(i, j int) bool { return byNode[node][i].ID < byNode[node][j].ID })
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })

	lines := []string{workspace}
	if len(nodeIDs) == 0 {
		lines = appendTreeLine(lines, "", true, "no workers")
		return joinLines(lines)
	}

	for i, nodeID := range nodeIDs {
		workers := byNode[nodeID]
		node, live := snapshot.Nodes[nodeID]
		nodeLine := workerNodeLine(nodeID, node, live, workers)
		lastNode := i == len(nodeIDs)-1
		lines = appendTreeLine(lines, "", lastNode, nodeLine)

		childPrefix := "|   "
		if lastNode {
			childPrefix = "    "
		}
		for j, worker := range workers {
			lines = appendTreeLine(lines, childPrefix, j == len(workers)-1, workerLine(worker, now))
		}
	}
	return joinLines(lines)
}

func workerNodeLine(id uint64, node pasta.NodeSnapshot, live bool, workers []almagest.WorkerSnapshot) string {
	name := node.Name
	class := node.Class
	status := ""
	if !live {
		if len(workers) > 0 {
			name = workers[0].Name
			class = workers[0].Class
		}
		status = " [gone]"
	} else if node.Placeholder {
		status = " [placeholder]"
	}
	if name == "" {
		name = "<unnamed>"
	}
	if class == "" {
		class = "<unknown>"
	}
	return fmt.Sprintf("node %d %s (%s)%s", id, name, class, status)
}

func workerLine(worker almagest.WorkerSnapshot, now time.Time) string {
	name := worker.WorkerName
	if name == "" {
		name = "<unnamed>"
	}
	return fmt.Sprintf("worker %d name=%q age=%s", worker.ID, name, workerAge(worker.StartedAt, now))
}

func workerAge(startedAt time.Time, now time.Time) string {
	if startedAt.IsZero() || now.Before(startedAt) {
		return "0s"
	}
	return now.Sub(startedAt).Round(time.Second).String()
}

func appendTreeLine(lines []string, prefix string, last bool, text string) []string {
	branch := "|-- "
	if last {
		branch = "`-- "
	}
	return append(lines, prefix+branch+text)
}

func joinLines(lines []string) string {
	n := 0
	for _, line := range lines {
		n += len(line) + 1
	}
	buf := make([]byte, 0, n)
	for _, line := range lines {
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	return string(buf)
}

func printWorkersHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  almagest-cli workers <command> [flags]

Commands:
  tree       render live workers attached under their parent nodes
  snapshot   print raw workers_snapshot API output

Examples:
  almagest-cli workers tree
  almagest-cli workers tree --workspace main
`)
}
