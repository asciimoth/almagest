//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/sandboxrole"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	sandboxisolated "github.com/asciimoth/almagest/bin/internal/subprogram/isolated"
	sandboxpolicy "github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	sandboxservice "github.com/asciimoth/almagest/bin/internal/subprogram/service"
	sandbox "github.com/asciimoth/gobox"
)

func isolatedSandboxSupervisorRole() bool {
	return os.Getenv("ALMAGEST_ROLE") == string(protocol.RoleSandboxSupervisor)
}

func bindSandboxSupervisor(file *os.File, peer roleentry.RuntimePeer) (transport.Stream, error) {
	return transport.BindSocket(file, transport.LinuxPeer{PID: peer.PID, UID: peer.UID, GID: peer.GID,
		OutsidePIDNamespace: peer.OutsidePIDNamespace})
}

func runIsolatedSandboxSupervisorRole() error {
	if len(os.Args) != 1 {
		return roleentry.ErrStandaloneRole
	}
	if err := applySandboxSupervisorLimits(); err != nil {
		return fmt.Errorf("apply sandbox supervisor limits: %w", err)
	}
	bootstrapStream, err := roleentry.OpenLane(roleentry.LaneBootstrap, os.Getenv)
	if err != nil {
		return err
	}
	defer bootstrapStream.Close() //nolint:errcheck
	controlFile, err := roleentry.OpenLaneFile(roleentry.LaneSandbox, os.Getenv)
	if err != nil {
		return err
	}
	defer func() {
		if controlFile != nil {
			_ = controlFile.Close()
		}
	}()
	dataFile, err := roleentry.OpenLaneFile(roleentry.LaneSandboxData, os.Getenv)
	if err != nil {
		return err
	}
	defer func() {
		if dataFile != nil {
			_ = dataFile.Close()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bootstrap, policyData, unavailable, err := sandboxrole.ReceivePolicy(ctx, bootstrapStream)
	if err != nil {
		return fmt.Errorf("receive sandbox supervisor policy: %w", err)
	}
	// Runtime-only discovery diagnostics travel beside the protected JSON. Add
	// them before NewHost normalizes the complete effective policy.
	config, err := sandboxpolicy.Parse(policyData)
	if err != nil {
		return fmt.Errorf("decode sandbox supervisor policy: %w", err)
	}
	config.UnavailableTemplates = make(map[sandbox.TemplateID]string, len(unavailable.Templates))
	for id, reason := range unavailable.Templates {
		config.UnavailableTemplates[sandbox.TemplateID(id)] = reason
	}
	config.UnavailablePrograms = make(map[sandbox.ProgramID]string, len(unavailable.Programs))
	for id, reason := range unavailable.Programs {
		config.UnavailablePrograms[sandbox.ProgramID(id)] = reason
	}
	service, err := sandboxservice.NewHost(config)
	if err != nil {
		return fmt.Errorf("construct sandbox supervisor service: %w", err)
	}
	binding, err := sandboxrole.ReceiveRuntimeBinding(ctx, bootstrapStream)
	if err != nil {
		_ = service.Close()
		return fmt.Errorf("receive sandbox runtime binding: %w", err)
	}
	peer := transport.LinuxPeer{PID: int32(binding.PID), UID: binding.UID, GID: binding.GID}
	control, err := transport.BindSocket(controlFile, peer)
	if err != nil {
		_ = service.Close()
		return fmt.Errorf("bind sandbox control peer: %w", err)
	}
	controlFile = nil
	data, err := transport.BindSocket(dataFile, peer)
	if err != nil {
		_ = control.Close()
		_ = service.Close()
		return fmt.Errorf("bind sandbox data peer: %w", err)
	}
	dataFile = nil
	server, err := sandboxisolated.NewServer(ctx, control, data, bootstrap.Session, service)
	if err != nil {
		_ = service.Close()
		return fmt.Errorf("start sandbox supervisor service: %w", err)
	}
	if err := roleentry.AcknowledgeBootstrap(ctx, bootstrapStream, bootstrap); err != nil {
		return errors.Join(fmt.Errorf("acknowledge sandbox supervisor: %w", err), server.Close())
	}
	stopped := make(chan error, 1)
	go func() {
		var shutdown struct{}
		stopped <- transport.ReadFrame(bootstrapStream, &shutdown)
		cancel()
	}()
	select {
	case <-server.Done():
		cancel()
		return server.Close()
	case err := <-stopped:
		return errors.Join(err, server.Close())
	}
}

func applySandboxSupervisorLimits() error {
	parse := func(name string) (uint64, error) {
		value, err := strconv.ParseUint(os.Getenv(name), 10, 64)
		if err != nil || value == 0 || strconv.FormatUint(value, 10) != os.Getenv(name) {
			return 0, protocol.ErrInvalidContract
		}
		return value, nil
	}
	files, err := parse("ALMAGEST_SANDBOX_NOFILE")
	if err != nil {
		return err
	}
	size, err := parse("ALMAGEST_SANDBOX_FSIZE")
	if err != nil {
		return err
	}
	for _, item := range []struct {
		resource int
		value    uint64
	}{
		{syscall.RLIMIT_CORE, 0}, {syscall.RLIMIT_NOFILE, files}, {syscall.RLIMIT_FSIZE, size},
	} {
		limit := syscall.Rlimit{Cur: item.value, Max: item.value}
		if err := syscall.Setrlimit(item.resource, &limit); err != nil {
			return err
		}
	}
	return nil
}
