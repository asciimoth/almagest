// Package service composes durable owner identity, lifecycle management, and
// a platform backend into the daemon's sandbox service.
package service

import (
	"context"
	"errors"
	"os/user"
	"runtime"
	"sync"

	"github.com/asciimoth/almagest/bin/internal/subprogram/direct"
	"github.com/asciimoth/almagest/bin/internal/subprogram/manager"
	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	"github.com/asciimoth/almagest/lib/sandbox"
)

// NewHost constructs the platform probe from the current supervisor identity.
// Native backends still perform their own authoritative prerequisite probes.
func NewHost(config policy.Config) (*Service, error) {
	identity := "unknown"
	if current, err := user.Current(); err == nil && current.Uid != "" {
		identity = current.Uid
	}
	return NewDirect(config, policy.Probe{Platform: runtime.GOOS, EffectiveIdentity: identity,
		Available: map[sandbox.Feature]bool{sandbox.Environment: true, sandbox.HandleInheritance: true},
		Reasons:   map[sandbox.Feature]string{sandbox.WritableStorage: "direct-child-storage-not-confined"}})
}

type Service struct {
	mu       sync.Mutex
	registry *registry
	manager  *manager.Manager
	backend  *direct.Backend
	status   sandbox.ServiceStatus
	failure  error
	closed   bool
}

var _ sandbox.Service = (*Service)(nil)
var _ sandbox.OwnerIssuer = (*Service)(nil)
var _ sandbox.OwnerBinder = (*Service)(nil)
var _ sandbox.OwnerRevoker = (*Service)(nil)
var _ sandbox.StatusReporter = (*Service)(nil)

func NewDirect(config policy.Config, probe policy.Probe) (*Service, error) {
	normalized, err := policy.Normalize(config)
	if err != nil {
		return nil, err
	}
	backend, err := direct.NewBackend(normalized, probe)
	if err != nil {
		return nil, err
	}
	registry, err := openRegistry(normalized.RegistryRoot, normalized.Limits.Receipts)
	if err != nil {
		return nil, err
	}
	ownerLimit := normalized.Limits.Instances * 4
	if ownerLimit > 65536 {
		ownerLimit = 65536
	}
	manager, err := manager.New(backend, registry, manager.Limits{Owners: ownerLimit, Instances: normalized.Limits.Instances, Receipts: normalized.Limits.Receipts})
	if err != nil {
		_ = registry.closeLock()
		return nil, err
	}
	return &Service{registry: registry, manager: manager, backend: backend, status: backend.ServiceStatus()}, nil
}

func (s *Service) ServiceStatus() sandbox.ServiceStatus {
	result := s.status.Clone()
	result.ActiveOwners, result.OrphanedOwners, result.QuarantinedOwners = s.registry.counts()
	return result
}

func (s *Service) IssueOwner(_ context.Context, proposed sandbox.Owner) (sandbox.Owner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return sandbox.Owner{}, sandbox.ErrClosed
	}
	if s.failure != nil {
		return sandbox.Owner{}, errors.Join(sandbox.ErrUnavailable, s.failure)
	}
	return s.registry.issue(proposed)
}
func (s *Service) BindOwner(ctx context.Context, owner sandbox.Owner) (sandbox.OwnerSandboxes, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, sandbox.ErrClosed
	}
	if s.failure != nil {
		s.mu.Unlock()
		return nil, errors.Join(sandbox.ErrUnavailable, s.failure)
	}
	s.mu.Unlock()
	return s.manager.BindOwner(ctx, owner)
}
func (s *Service) IssueAndBindOwner(ctx context.Context, proposed sandbox.Owner) (sandbox.Owner, sandbox.OwnerSandboxes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return sandbox.Owner{}, nil, sandbox.ErrClosed
	}
	if s.failure != nil {
		return sandbox.Owner{}, nil, errors.Join(sandbox.ErrUnavailable, s.failure)
	}
	owner, err := s.registry.issue(proposed)
	if err != nil {
		return sandbox.Owner{}, nil, err
	}
	view, err := s.manager.BindOwner(ctx, owner)
	if err != nil {
		releaseErr := s.registry.release(owner)
		if releaseErr != nil {
			s.failure = errors.Join(err, releaseErr)
		}
		return sandbox.Owner{}, nil, errors.Join(err, releaseErr)
	}
	return owner, view, nil
}
func (s *Service) RebindOwner(ctx context.Context, request sandbox.OwnerRename) (sandbox.Owner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return sandbox.Owner{}, sandbox.ErrClosed
	}
	if s.failure != nil {
		return sandbox.Owner{}, errors.Join(sandbox.ErrUnavailable, s.failure)
	}
	updated, err := s.registry.rename(request)
	if err != nil {
		return sandbox.Owner{}, err
	}
	result, err := s.manager.RebindOwner(ctx, request)
	if err != nil {
		rollbackErr := s.registry.rollbackRename(request.OperationID, updated)
		if rollbackErr != nil {
			s.failure = errors.Join(err, rollbackErr)
		}
		return sandbox.Owner{}, errors.Join(err, rollbackErr)
	}
	if result != updated {
		s.failure = sandbox.ErrStale
		return sandbox.Owner{}, sandbox.ErrStale
	}
	return result, nil
}

func (s *Service) RevokeOwner(ctx context.Context, owner sandbox.Owner) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return sandbox.ErrClosed
	}
	if s.failure != nil {
		return errors.Join(sandbox.ErrUnavailable, s.failure)
	}
	return s.manager.ReleaseOwner(ctx, owner)
}

func (s *Service) ReleaseOwner(ctx context.Context, owner sandbox.Owner) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return sandbox.ErrClosed
	}
	if s.failure != nil {
		return errors.Join(sandbox.ErrUnavailable, s.failure)
	}
	managerErr := s.manager.ReleaseOwner(ctx, owner)
	if managerErr != nil && !errors.Is(managerErr, sandbox.ErrClosed) {
		return managerErr
	}
	if managerErr = s.manager.WaitOwner(ctx, owner); managerErr != nil {
		return managerErr
	}
	if err := s.backend.PurgeOwner(owner); err != nil {
		s.failure = err
		return err
	}
	if err := s.registry.release(owner); err != nil {
		s.failure = err
		return err
	}
	return nil
}
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	failure := s.failure
	s.mu.Unlock()
	managerErr := s.manager.Close()
	var registryErr error
	if managerErr == nil {
		registryErr = s.registry.deactivateAll()
	}
	return errors.Join(failure, managerErr, registryErr, s.registry.closeLock())
}
