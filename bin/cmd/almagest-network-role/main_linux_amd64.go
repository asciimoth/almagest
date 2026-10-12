//go:build linux && amd64

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/alexflint/go-arg"
	"github.com/asciimoth/almagest/bin/internal/daemonargs"
	"github.com/asciimoth/almagest/bin/internal/isolation/bootstrap"
	"github.com/asciimoth/almagest/bin/internal/isolation/networkrole"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

const (
	defaultConfigPath    = "/var/lib/almagest/config/alm.hujson"
	defaultProtectedPath = "/etc/almagest/protected_alm.hujson"
	defaultRuntimePath   = "/var/lib/almagest/runtime"
	defaultHelperPath    = "/var/lib/almagest/helper"
	defaultBrokerPath    = "/var/lib/almagest/broker"
	developmentRootEnv   = "ALMAGEST_ISOLATION_DEVELOPMENT_ROOT"
	developmentIDsEnv    = "ALMAGEST_ISOLATION_DEVELOPMENT_IDENTITIES"
)

func runNetworkRole() error {
	var arguments daemonargs.Args
	if err := arg.Parse(&arguments); err != nil {
		return err
	}
	if arguments.NoIsolation {
		return errors.New("almagest-network-role cannot run direct mode; invoke almagestd --no-isolation")
	}
	if arguments.Experimental {
		return errors.New("--experimental requires explicit --no-isolation")
	}
	developmentRoot := os.Getenv(developmentRootEnv)
	if developmentRoot != "" && os.Geteuid() != 0 {
		return errors.New("isolated development root requires a root network role")
	}
	production, err := linuxProduction(arguments, currentExecutable, user.Lookup, developmentRoot, os.Getenv(developmentIDsEnv))
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	err = networkrole.RunLinuxProduction(ctx, production, func() {
		_, _ = fmt.Fprintf(os.Stderr, "Execution mode: isolation; protocol=%d; runtime=linux-namespaces+landlock+seccomp; broker=trusted-native\n", protocol.Version)
	})
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func currentExecutable() (string, error) {
	path, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) || strings.HasSuffix(path, " (deleted)") {
		return "", errors.New("network role executable has no stable installed path")
	}
	return filepath.Clean(path), nil
}

func linuxProduction(arguments daemonargs.Args, executable func() (string, error), lookup func(string) (*user.User, error), developmentRoot, developmentIDs string) (networkrole.LinuxProduction, error) {
	if executable == nil || developmentRoot == "" && lookup == nil {
		return networkrole.LinuxProduction{}, errors.New("missing Linux bootstrap dependency")
	}
	self, err := executable()
	if err != nil {
		return networkrole.LinuxProduction{}, err
	}
	self, err = filepath.Abs(self)
	if err != nil {
		return networkrole.LinuxProduction{}, err
	}
	bin := filepath.Dir(self)
	if developmentRoot != "" {
		if !filepath.IsAbs(developmentRoot) || filepath.Clean(developmentRoot) != developmentRoot {
			return networkrole.LinuxProduction{}, errors.New("isolated development root must be absolute and clean")
		}
		if arguments.Config != "" || arguments.ProtectedConfig != "" || arguments.FileStorage != "" {
			return networkrole.LinuxProduction{}, errors.New("isolated development root cannot be combined with config or storage path flags")
		}
		arguments.Config = filepath.Join(developmentRoot, "config", "alm.hujson")
		arguments.ProtectedConfig = filepath.Join(developmentRoot, "protected", "protected_alm.hujson")
		arguments.FileStorage = filepath.Join(developmentRoot, "runtime")
	}
	configPath, err := absolutePath(arguments.Config, defaultConfigPath)
	if err != nil {
		return networkrole.LinuxProduction{}, err
	}
	if info, statErr := os.Stat(configPath); statErr == nil && info.IsDir() {
		configPath = filepath.Join(configPath, "alm.hujson")
	}
	protectedPath, err := absolutePath(arguments.ProtectedConfig, defaultProtectedPath)
	if err != nil {
		return networkrole.LinuxProduction{}, err
	}
	runtimePath, err := absolutePath(arguments.FileStorage, defaultRuntimePath)
	if err != nil {
		return networkrole.LinuxProduction{}, err
	}
	identities := networkrole.LinuxProductionIdentities{}
	if developmentRoot != "" {
		identities, err = parseDevelopmentIdentities(developmentIDs)
		if err != nil {
			return networkrole.LinuxProduction{}, err
		}
	} else {
		if developmentIDs != "" {
			return networkrole.LinuxProduction{}, errors.New("development identities require an isolated development root")
		}
		for name, output := range map[string]*networkrole.LinuxIdentity{
			"almagest-runtime": &identities.Runtime,
			"almagest-writer":  &identities.Writer,
			"almagest-command": &identities.Helper,
		} {
			account, lookupErr := lookup(name)
			if lookupErr != nil {
				return networkrole.LinuxProduction{}, fmt.Errorf("required isolation account %s: %w; install the system profile or use explicit --no-isolation", name, lookupErr)
			}
			uid, uidErr := strconv.ParseUint(account.Uid, 10, 32)
			gid, gidErr := strconv.ParseUint(account.Gid, 10, 32)
			if uidErr != nil || gidErr != nil || uid == 0 || gid == 0 {
				return networkrole.LinuxProduction{}, fmt.Errorf("required isolation account %s has an invalid non-root identity", name)
			}
			*output = networkrole.LinuxIdentity{UID: uint32(uid), GID: uint32(gid)}
		}
	}
	arguments.Config, arguments.ProtectedConfig, arguments.FileStorage = configPath, protectedPath, runtimePath
	sources := bootstrap.ConfigSources{RegularPath: configPath, ProtectedPath: protectedPath,
		CombinedStdin: arguments.StdInConfig, DefaultRegular: []byte("{}"), DefaultProtected: []byte("{}")}
	helperPath, brokerPath := defaultHelperPath, defaultBrokerPath
	layout := bootstrap.StorageLayout{Legacy: "/var/lib/almagest/store", Runtime: runtimePath, Broker: defaultBrokerPath,
		Backup: "/var/lib/almagest/store.pre-isolation", State: "/var/lib/almagest/isolation-migration.json"}
	if developmentRoot != "" {
		helperPath = filepath.Join(developmentRoot, "helper")
		brokerPath = filepath.Join(developmentRoot, "broker")
		layout = bootstrap.StorageLayout{Legacy: filepath.Join(developmentRoot, "legacy-store"), Runtime: runtimePath,
			Broker: brokerPath, Backup: filepath.Join(developmentRoot, "legacy-store.pre-isolation"),
			State: filepath.Join(developmentRoot, "isolation-migration.json")}
	}
	return networkrole.LinuxProduction{Arguments: arguments, Sources: sources, Identities: identities, Stdin: os.Stdin,
		LookupEnv: os.LookupEnv, Output: os.Stderr, Paths: networkrole.LinuxProductionPaths{
			Launcher: filepath.Join(bin, "almagest-linux-role-launcher"), RuntimeExecutable: filepath.Join(bin, "almagestd"),
			WriterExecutable: filepath.Join(bin, "almagest-config-writer-role"), HelperExecutable: filepath.Join(bin, "almagest-command-role"),
			RuntimeDirectory: runtimePath, WriterDirectory: filepath.Dir(configPath), HelperDirectory: helperPath, BrokerDirectory: brokerPath,
			Layout: layout,
		}}, nil
}

func parseDevelopmentIdentities(value string) (networkrole.LinuxProductionIdentities, error) {
	parts := strings.Split(value, ",")
	if len(parts) != 3 {
		return networkrole.LinuxProductionIdentities{}, errors.New("isolated development identities must contain runtime, writer, and command UID/GID pairs")
	}
	parsed := make([]networkrole.LinuxIdentity, len(parts))
	seenUIDs, seenGIDs := map[uint32]bool{}, map[uint32]bool{}
	for index, part := range parts {
		values := strings.Split(part, ":")
		if len(values) != 2 {
			return networkrole.LinuxProductionIdentities{}, errors.New("isolated development identity must be UID:GID")
		}
		uid, uidErr := strconv.ParseUint(values[0], 10, 32)
		gid, gidErr := strconv.ParseUint(values[1], 10, 32)
		if uidErr != nil || gidErr != nil || uid == 0 || gid == 0 || uid == uint64(^uint32(0)) || gid == uint64(^uint32(0)) {
			return networkrole.LinuxProductionIdentities{}, errors.New("isolated development identity must use valid non-root UID and GID values")
		}
		identity := networkrole.LinuxIdentity{UID: uint32(uid), GID: uint32(gid)}
		if seenUIDs[identity.UID] || seenGIDs[identity.GID] {
			return networkrole.LinuxProductionIdentities{}, errors.New("isolated development identities must use distinct UID and GID values")
		}
		seenUIDs[identity.UID], seenGIDs[identity.GID] = true, true
		parsed[index] = identity
	}
	return networkrole.LinuxProductionIdentities{Runtime: parsed[0], Writer: parsed[1], Helper: parsed[2]}, nil
}

func absolutePath(value, fallback string) (string, error) {
	if value == "" {
		value = fallback
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}
