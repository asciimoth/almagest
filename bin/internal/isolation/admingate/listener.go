package admingate

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
)

type EndpointKind string

const (
	EndpointWeb  EndpointKind = "web"
	EndpointUnix EndpointKind = "unix"
	EndpointPipe EndpointKind = "pipe"
	maxFilters                = 128
)

// Filters are fixed protected peer-owner rules. They are evaluated in N only.
type Filters struct {
	UIDs      []uint32
	GIDs      []uint32
	Usernames []string
}

func (f Filters) enabled() bool {
	return len(f.UIDs) != 0 || len(f.GIDs) != 0 || len(f.Usernames) != 0
}

// EndpointConfig is resolved from protected configuration before R starts.
// R cannot add endpoints or change their paths, addresses, or filters.
type EndpointConfig struct {
	ID      uint16
	Kind    EndpointKind
	Address string
	Group   string
	Filters Filters
}

// Listen opens one fixed public endpoint and installs its native peer gate.
func Listen(config EndpointConfig) (net.Listener, error) {
	if err := validateListenerConfig(config); err != nil {
		return nil, err
	}
	switch config.Kind {
	case EndpointWeb:
		return listenWeb(config.Address, config.Filters)
	case EndpointUnix:
		return listenUnix(config.Address, config.Group, config.Filters)
	case EndpointPipe:
		return listenPipe(config.Address, config.Filters)
	default:
		return nil, fmt.Errorf("%w: admin endpoint kind %q", errors.ErrUnsupported, config.Kind)
	}
}

// ListenAll opens all endpoints transactionally. A later failure closes every
// endpoint that this call already opened.
func ListenAll(configs []EndpointConfig) ([]Endpoint, error) {
	if len(configs) == 0 || len(configs) > MaxEndpoints {
		return nil, fmt.Errorf("admin endpoint count: %w", errors.ErrUnsupported)
	}
	result := make([]Endpoint, 0, len(configs))
	seen := make(map[uint16]struct{}, len(configs))
	for _, config := range configs {
		if _, exists := seen[config.ID]; exists || config.ID == 0 {
			closeEndpoints(result)
			return nil, fmt.Errorf("duplicate or zero admin endpoint ID %d", config.ID)
		}
		seen[config.ID] = struct{}{}
		listener, err := Listen(config)
		if err != nil {
			closeEndpoints(result)
			return nil, fmt.Errorf("listen on admin endpoint %d: %w", config.ID, err)
		}
		result = append(result, Endpoint{ID: config.ID, Listener: listener})
	}
	return result, nil
}

func closeEndpoints(endpoints []Endpoint) {
	for _, endpoint := range endpoints {
		_ = endpoint.Listener.Close()
	}
}

func validateListenerConfig(config EndpointConfig) error {
	if config.ID == 0 || config.Address == "" {
		return fmt.Errorf("admin endpoint ID and address must be set")
	}
	if len(config.Filters.UIDs)+len(config.Filters.GIDs)+len(config.Filters.Usernames) > maxFilters {
		return fmt.Errorf("admin peer filter count exceeds %d", maxFilters)
	}
	for _, name := range config.Filters.Usernames {
		if name == "" || len(name) > 256 {
			return fmt.Errorf("admin peer username is empty or too long")
		}
	}
	if config.Kind == EndpointWeb {
		host, port, err := net.SplitHostPort(config.Address)
		if err != nil {
			return fmt.Errorf("web admin address: %w", err)
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.IsLoopback() {
			return fmt.Errorf("web admin address must contain a numeric loopback IP")
		}
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return fmt.Errorf("web admin port: %w", err)
		}
	}
	return nil
}
