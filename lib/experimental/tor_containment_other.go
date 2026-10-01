//go:build !windows

package experimentalnodes

import (
	tor "github.com/asciimoth/tor-driver"
	"github.com/asciimoth/tor-driver/direct"
)

func newXTorProcessPolicy(system *direct.BestEffortSystem) (tor.Processes, error) {
	return system, nil
}
