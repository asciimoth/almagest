package platform

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/asciimoth/almagest/bin/almagestd/args"
	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	"github.com/asciimoth/almagest/bin/almagestd/daemonservices/localconfig"
	"github.com/kataras/golog"
	tailscalehujson "github.com/tailscale/hujson"
)

var ErrConfigReadOnly = errors.New("config source is read-only")

// ConfigBootstrap contains startup-only config inputs and the optional local
// persistence service. Runtime code receives the values, not the source.
type ConfigBootstrap struct {
	Regular       []byte
	Protected     []byte
	Persistence   daemonservices.ConfigPersistence
	RegularPath   string
	ProtectedPath string
}

func (c ConfigBootstrap) ConfigFilePath() string          { return c.RegularPath }
func (c ConfigBootstrap) ProtectedConfigFilePath() string { return c.ProtectedPath }

// PrepareConfig reads trusted bootstrap inputs and binds regular-config
// persistence to the startup-selected target. Read-only sources have no
// persistence service.
func PrepareConfig(arguments args.Args) (ConfigBootstrap, error) {
	agent, err := prepareAgent(arguments)
	if err != nil {
		return ConfigBootstrap{}, err
	}
	regular, protected, err := agent.ReadConfig()
	if err != nil {
		return ConfigBootstrap{}, err
	}
	bootstrap := ConfigBootstrap{Regular: regular, Protected: protected}
	bootstrap.RegularPath = agent.ConfigFilePath()
	bootstrap.ProtectedPath = agent.ProtectedConfigFilePath()
	if !agent.ConfRO() {
		bootstrap.Persistence = localconfig.New(agent.ReadRegularConfig, agent.WriteConfig)
	}
	return bootstrap, nil
}

// FSCleanup releases resources owned by a file storage setup.
//
// Persistent storage returns a no-op cleanup. Temporary storage returns a
// cleanup that removes the temporary storage directory.
type FSCleanup func() error

func noopFSCleanup() error {
	return nil
}

// ReadStdinIfPiped returns fallback immediately if stdin is a terminal.
// Otherwise, reads all piped/file input until EOF.
func readStdinIfPiped(fallback []byte) ([]byte, error) {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return nil, err
	}

	if stat.Mode()&os.ModeCharDevice != 0 {
		return fallback, nil
	}

	// Stdin is a pipe or redirected file → read until EOF
	return io.ReadAll(os.Stdin)
}

func splitEmbeddedProtectedConfig(
	configData, defaultProtectedData []byte,
) (mainData, protectedData []byte) {
	config, err := tailscalehujson.Parse(configData)
	if err != nil {
		return configData, defaultProtectedData
	}

	root, ok := config.Value.(*tailscalehujson.Object)
	if !ok {
		return configData, defaultProtectedData
	}

	i := objectMemberIndex(root, "Protected")
	if i < 0 {
		return configData, defaultProtectedData
	}

	protected := root.Members[i].Value.Clone()
	copy(root.Members[i:], root.Members[i+1:])
	root.Members = root.Members[:len(root.Members)-1]

	return config.Pack(), protected.Pack()
}

func objectMemberIndex(obj *tailscalehujson.Object, key string) int {
	for i, member := range obj.Members {
		var name string
		value := member.Name.Clone()
		value.Standardize()
		if err := json.Unmarshal(value.Pack(), &name); err == nil && name == key {
			return i
		}
	}
	return -1
}

func DropPrivileges() {
	golog.Warn("Privileges dropping not yet implemented for this platform")
}
