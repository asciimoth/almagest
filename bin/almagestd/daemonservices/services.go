// Package daemonservices defines the local service contracts used to compose
// almagestd. These contracts describe in-process Go calls. They are not a wire
// protocol and do not define transport messages or remote resource handles.
package daemonservices

import (
	"github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/gonnect/sysnet"
)

// HostNetwork is the local host-network service contract. Direct mode keeps
// the full sysnet surface and returns backend networks and TUNs unchanged so
// native fast paths stay intact. This local contract is not an IPC schema.
type HostNetwork interface {
	sysnet.System
}

// ConfigSource supplies the two startup configuration documents.
type ConfigSource interface {
	ReadConfig() (config, protected []byte, err error)
	// ConfRO reports whether the regular configuration source is read-only.
	// The name is retained from the existing platform contract.
	ConfRO() bool
}

// ConfigPersistence writes the regular configuration to its fixed startup
// target. The target is selected before this service is constructed.
type ConfigPersistence interface {
	WriteConfig(config []byte) error
}

// ConfigStore combines startup reads and regular-config persistence.
type ConfigStore interface {
	ConfigSource
	ConfigPersistence
}

// AllowedCommands dispatches requests from the protected startup allowlist.
// It intentionally has no method that accepts an executable or argv.
type AllowedCommands interface {
	Submit(command.Request) bool
}

// Lifecycle owns one composed application and releases it exactly once.
type Lifecycle interface {
	Close() error
}
