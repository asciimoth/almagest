//go:build !almagest_e2e

package experimentalnodes

import tor "github.com/asciimoth/tor-driver"

func applyXTorE2EDependencies(deps tor.Dependencies) tor.Dependencies { return deps }
