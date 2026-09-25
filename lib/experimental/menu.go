package experimentalnodes

import (
	"slices"
	"strings"

	"github.com/asciimoth/formular"
	tor "github.com/asciimoth/tor-driver"

	"github.com/asciimoth/almagest/lib/nodekit"
)

func (n *xTorDriverNode) sendMenuSnapshot(force bool) {
	blocks := []formular.Block{n.statusBlock(), n.controlBlock()}
	blocks = append(blocks, n.configBlocks()...)
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, force, blocks...)
}

func (n *xTorDriverNode) sendStatusBlock() {
	nodekit.SendNodeBlockSnapshot(n.W, n.ID, n.statusBlock())
}

func (n *xTorDriverNode) statusBlock() formular.Block {
	bootstrap := min(uint(n.bootstrap.Progress), 100)
	transport := ""
	if n.transport.Transport != "" {
		transport = string(n.transport.Transport) + ": " + xTorTransportState(n.transport.State)
	}
	return formular.Block{ID: xTorStatusBlockID, Order: 10, Generation: 1, Items: []formular.Item{
		nodekit.ReadonlyTextField("state", "State", n.stateText()),
		{Type: formular.ItemProgressbar, ID: "bootstrap", Label: "Bootstrap", Progress: &bootstrap},
		nodekit.ReadonlyTextField("transport", "Managed transport", transport),
		nodekit.ReadonlyBoolField("outboundAvailable", "Outbound attached", n.outboundState.Available),
		nodekit.ReadonlyStringListField("services", "Onion services", n.serviceStatusLines()),
		nodekit.ReadonlyTextField("error", "Error", firstNonempty(n.formError, n.runtimeError)),
	}}
}

func (n *xTorDriverNode) controlBlock() formular.Block {
	button := formular.Item{
		Type: formular.ItemButton, ID: xTorRestartButtonID, Label: "Restart Tor",
		Help: "Close the current owned Tor process and build a new runtime from the saved node configuration.",
	}
	return formular.Block{ID: xTorControlBlockID, Order: 20, Generation: 1, Items: []formular.Item{button}}
}

func (n *xTorDriverNode) stateText() string {
	switch {
	case !n.config.Enabled:
		return "disabled"
	case n.formError != "":
		return "error"
	case n.pending:
		return "starting"
	case n.runtimeError != "":
		return "error"
	case n.activeID == 0:
		return "stopped"
	case !n.outboundState.Available || !nodekit.NetworkIsReady(n.currentOutboundNetwork()):
		return "waiting for Network"
	case !n.ready:
		return "bootstrapping"
	case n.servicePublicationFailed():
		return "degraded"
	case n.servicePublicationPending():
		return "publishing onion services"
	default:
		return "up"
	}
}

func (n *xTorDriverNode) updateLabel() error { return n.SetLabel(n.stateText()) }

func (n *xTorDriverNode) servicePublicationPending() bool {
	for _, status := range n.services {
		if status.Publication == tor.PublicationPending {
			return true
		}
	}
	return false
}

func (n *xTorDriverNode) servicePublicationFailed() bool {
	for _, status := range n.services {
		if status.Publication == tor.PublicationFailed {
			return true
		}
	}
	return false
}

func (n *xTorDriverNode) serviceStatusLines() []string {
	lines := make([]string, 0, len(n.services))
	for name, status := range n.services {
		line := name + " | " + status.Address + " | " + xTorPublicationState(status.Publication)
		if status.Problem != "" {
			line += " | " + status.Problem
		}
		lines = append(lines, line)
	}
	slices.Sort(lines)
	return lines
}

func xTorPublicationState(state tor.PublicationState) string {
	switch state {
	case tor.PublicationPublished:
		return "published"
	case tor.PublicationFailed:
		return "failed"
	default:
		return "pending"
	}
}

func xTorTransportState(state tor.TransportState) string {
	switch state {
	case tor.TransportReady:
		return "ready"
	case tor.TransportFailed:
		return "failed"
	default:
		return "starting"
	}
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
