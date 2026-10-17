package tornodes

import (
	"slices"

	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/links"
)

const (
	torNetworkKeyPrefix = "network:"
	torServiceKeyPrefix = "service:"
)

func torNetworkKey(name string) string { return torNetworkKeyPrefix + name }
func torServiceKey(name string) string { return torServiceKeyPrefix + name }

func torNetworkPort(name string) pasta.Port {
	return pasta.Port{Direction: "left", Name: "Network " + name, Types: []string{links.TypeNetwork}}
}

func torServicePort(name string) pasta.Port {
	return pasta.Port{Direction: "left", Name: "Onion " + name, Types: []string{links.TypeNetwork}}
}

func torDesiredPorts(config TorDriverConfig) []struct {
	key  string
	port pasta.Port
} {
	out := make([]struct {
		key  string
		port pasta.Port
	}, 0, len(config.Networks)+len(config.Services))
	for _, network := range config.Networks {
		out = append(out, struct {
			key  string
			port pasta.Port
		}{key: torNetworkKey(network.Name), port: torNetworkPort(network.Name)})
	}
	for _, service := range config.Services {
		out = append(out, struct {
			key  string
			port pasta.Port
		}{key: torServiceKey(service.Name), port: torServicePort(service.Name)})
	}
	return out
}

func reconcileTorClassState(config TorDriverConfig, state *pasta.NodeClassState) {
	if state == nil {
		return
	}
	byName := make(map[string]pasta.Port, len(state.LeftPorts))
	for _, port := range state.LeftPorts {
		byName[port.Name] = port
	}
	left := make([]pasta.Port, 0, len(config.Networks)+len(config.Services))
	for _, desired := range torDesiredPorts(config) {
		port := desired.port
		if existing, ok := byName[port.Name]; ok {
			port.ID = existing.ID
			port.Node = existing.Node
			port.Links = append([]uint64(nil), existing.Links...)
		}
		left = append(left, port)
	}
	right := nodekitNetworkPortRight()
	for _, existing := range state.RightPorts {
		if existing.Name == torOutboundPortName {
			right.ID = existing.ID
			right.Node = existing.Node
			right.Links = append([]uint64(nil), existing.Links...)
			break
		}
	}
	state.PrimaryType = links.TypeNetwork
	state.LeftPorts = left
	state.RightPorts = []pasta.Port{right}
}

func nodekitNetworkPortRight() pasta.Port {
	return pasta.Port{Direction: "right", Name: torOutboundPortName, Types: []string{links.TypeNetwork}}
}

func (n *torDriverNode) rebuildPorts() error {
	desired := torDesiredPorts(n.config)
	desiredByName := make(map[string]string, len(desired))
	for _, item := range desired {
		desiredByName[item.port.Name] = item.key
	}
	n.outputPorts = make(map[string]uint64, len(desired))
	n.portOutputs = make(map[uint64]string, len(desired))

	snapshot, ok := n.W.NodeSnapshotLocked(n.ID)
	if !ok {
		return pasta.ErrNoNode
	}
	for _, portID := range snapshot.LeftPorts {
		port, ok := n.W.PortSnapshotLocked(portID)
		if !ok {
			continue
		}
		key := desiredByName[port.Name]
		if key == "" {
			n.W.RemovePortLocked(portID)
			continue
		}
		n.outputPorts[key] = portID
		n.portOutputs[portID] = key
		if !slices.Equal(port.Types, []string{links.TypeNetwork}) {
			if err := n.W.SetPortTypesLocked(portID, []string{links.TypeNetwork}); err != nil {
				return err
			}
		}
	}
	for _, item := range desired {
		if n.outputPorts[item.key] != 0 {
			continue
		}
		port := item.port
		port.Node = n.ID
		id, err := n.W.AddPortLocked(port)
		if err != nil {
			return err
		}
		n.outputPorts[item.key] = id
		n.portOutputs[id] = item.key
	}

	if n.outbound == 0 {
		for _, portID := range snapshot.RightPorts {
			port, ok := n.W.PortSnapshotLocked(portID)
			if ok && port.Name == torOutboundPortName {
				n.outbound = portID
				break
			}
		}
	}
	for _, portID := range snapshot.RightPorts {
		if portID != n.outbound {
			n.W.RemovePortLocked(portID)
		}
	}
	if n.outbound == 0 {
		port := nodekitNetworkPortRight()
		port.Node = n.ID
		id, err := n.W.AddPortLocked(port)
		if err != nil {
			return err
		}
		n.outbound = id
	}
	if port, ok := n.W.PortSnapshotLocked(n.outbound); ok {
		if port.Name != torOutboundPortName {
			if err := n.W.SetPortNameLocked(n.outbound, torOutboundPortName); err != nil {
				return err
			}
		}
		if !slices.Equal(port.Types, []string{links.TypeNetwork}) {
			if err := n.W.SetPortTypesLocked(n.outbound, []string{links.TypeNetwork}); err != nil {
				return err
			}
		}
	}
	leftOrder := make([]uint64, 0, len(desired))
	for _, item := range desired {
		leftOrder = append(leftOrder, n.outputPorts[item.key])
	}
	if err := n.W.SetNodePortOrderLocked(n.ID, "left", leftOrder); err != nil {
		return err
	}
	return n.W.SetNodePortOrderLocked(n.ID, "right", []uint64{n.outbound})
}

func (n *torDriverNode) sendOutputToLink(key string, link uint64, refresh bool) {
	if !n.ready || n.activeSources == nil {
		return
	}
	source := n.activeSources[key]
	if source == nil {
		return
	}
	if refresh {
		n.outputs.RefreshLocked(n.W, n.ID, link, source)
		return
	}
	n.outputs.SendLocked(n.W, n.ID, link, source)
}

func (n *torDriverNode) sendAllOutputs() {
	if !n.ready {
		return
	}
	for key, port := range n.outputPorts {
		source := n.activeSources[key]
		if source != nil {
			n.outputs.SendPortLocked(n.W, n.ID, port, source)
		}
	}
}
