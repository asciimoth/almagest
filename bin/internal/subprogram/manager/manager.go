// Package manager coordinates owner and instance lifetimes. Native backends
// must close all resources on context cancellation and prove tree exit in Close.
package manager

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"

	sandbox "github.com/asciimoth/gobox"
)

// Authority checks a binding against a host-owned durable registry. Graph
// metadata alone is not authority to open an existing persistent directory.
type Authority interface {
	Authorize(context.Context, sandbox.Owner) error
}

// Backend is trusted host code, never supplied by a node. Prepare must select
// and verify a fixed plan before it returns. It must dispose of partial setup
// on error. Close must return only after cleanup, or return an error that makes
// the owner unusable. Cancellation revokes pending operations and all handles.
type Backend interface {
	Prepare(context.Context, sandbox.Owner, string, sandbox.TemplateID, uint64) (sandbox.Sandbox, error)
}

type Limits struct{ Owners, Instances, Receipts int }
type ownerKey struct{ workspace, key string }
type nameKey struct{ workspace, name string }
type receipt struct {
	request sandbox.OwnerRename
	result  sandbox.Owner
}
type slot struct {
	template   sandbox.TemplateID
	instance   sandbox.Sandbox
	generation uint64
}
type owner struct {
	operation chan struct{}
	value     sandbox.Owner
	ctx       context.Context
	cancel    context.CancelFunc
	slots     map[string]*slot
	revoked   bool
	cleanup   error
	done      chan struct{}
}

type Manager struct {
	mu             sync.Mutex
	backend        Backend
	authority      Authority
	limits         Limits
	owners         map[ownerKey]*owner
	names          map[nameKey]ownerKey
	receipts       map[string]receipt
	nextGeneration uint64
	instances      int
	closed         bool
	closeDone      chan struct{}
	closeErr       error
}

var _ sandbox.Service = (*Manager)(nil)

func New(backend Backend, authority Authority, limits Limits) (*Manager, error) {
	if backend == nil || authority == nil || limits.Owners < 1 || limits.Instances < 1 || limits.Receipts < 1 || limits.Owners > 65536 || limits.Instances > 65536 || limits.Receipts > 1048576 {
		return nil, sandbox.ErrInvalid
	}
	return &Manager{backend: backend, authority: authority, limits: limits, owners: map[ownerKey]*owner{}, names: map[nameKey]ownerKey{}, receipts: map[string]receipt{}, closeDone: make(chan struct{})}, nil
}
func key(o sandbox.Owner) ownerKey { return ownerKey{o.Workspace, o.Key} }
func validOwner(o sandbox.Owner) bool {
	return sandbox.ValidateName(o.Key) == nil && sandbox.ValidateName(o.Workspace) == nil && o.Epoch > 0 && o.NameRevision > 0 && validLabel(o.Name) && validLabel(o.Class)
}
func validLabel(s string) bool {
	if len(s) == 0 || len(s) > 256 || !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

func (m *Manager) BindOwner(ctx context.Context, value sandbox.Owner) (sandbox.OwnerSandboxes, error) {
	if !validOwner(value) {
		return nil, sandbox.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.authority.Authorize(ctx, value); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.closed {
		return nil, sandbox.ErrClosed
	}
	k := key(value)
	if existing := m.owners[k]; existing != nil {
		if existing.value.Epoch >= value.Epoch {
			if existing.value != value {
				return nil, sandbox.ErrStale
			}
			if existing.revoked {
				return nil, sandbox.ErrClosed
			}
			return &bound{manager: m, owner: existing}, nil
		}
		// A new lifetime cannot reuse state until the old tree is proved stopped.
		if !existing.revoked {
			return nil, sandbox.ErrStale
		}
		select {
		case <-existing.done:
		default:
			return nil, sandbox.ErrUnavailable
		}
		if existing.cleanup != nil {
			return nil, sandbox.ErrUnavailable
		}
		if existing.value.Class != value.Class {
			return nil, sandbox.ErrDenied
		}
	}

	if _, exists := m.names[nameKey{value.Workspace, value.Name}]; exists {
		return nil, sandbox.ErrConflict
	}
	if m.owners[k] == nil && len(m.owners) >= m.limits.Owners {
		return nil, sandbox.ErrQuota
	}
	lifetime, cancel := context.WithCancel(context.Background())
	record := &owner{value: value, ctx: lifetime, cancel: cancel, slots: map[string]*slot{}, operation: make(chan struct{}, 1), done: make(chan struct{})}
	m.owners[k] = record
	m.names[nameKey{value.Workspace, value.Name}] = k
	return &bound{manager: m, owner: record}, nil
}

func (m *Manager) RebindOwner(ctx context.Context, request sandbox.OwnerRename) (sandbox.Owner, error) {
	if !validOwner(request.Owner) || !validLabel(request.NewName) || sandbox.ValidateName(request.OperationID) != nil {
		return sandbox.Owner{}, sandbox.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return sandbox.Owner{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return sandbox.Owner{}, sandbox.ErrClosed
	}
	if old, ok := m.receipts[request.OperationID]; ok {
		if old.request != request {
			return sandbox.Owner{}, sandbox.ErrConflict
		}
		return old.result, nil
	}
	record := m.owners[key(request.Owner)]
	if record == nil || record.revoked {
		return sandbox.Owner{}, sandbox.ErrClosed
	}
	if record.value != request.Owner {
		return sandbox.Owner{}, sandbox.ErrStale
	}
	if len(m.receipts) >= m.limits.Receipts {
		return sandbox.Owner{}, sandbox.ErrQuota
	}
	newName := nameKey{request.Owner.Workspace, request.NewName}
	if other, exists := m.names[newName]; exists && other != key(request.Owner) {
		return sandbox.Owner{}, sandbox.ErrConflict
	}
	if record.value.NameRevision == ^uint64(0) {
		return sandbox.Owner{}, sandbox.ErrQuota
	}
	delete(m.names, nameKey{record.value.Workspace, record.value.Name})
	record.value.Name = request.NewName
	record.value.NameRevision++
	m.names[newName] = key(record.value)
	m.receipts[request.OperationID] = receipt{request, record.value}
	return record.value, nil
}

func (m *Manager) ReleaseOwner(ctx context.Context, value sandbox.Owner) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	record := m.owners[key(value)]
	if record == nil {
		m.mu.Unlock()
		return sandbox.ErrClosed
	}
	if record.value.Epoch != value.Epoch {
		m.mu.Unlock()
		return sandbox.ErrStale
	}
	m.revokeLocked(record)
	m.mu.Unlock()
	// Revocation is synchronous. Native cleanup never waits under a graph lock.
	return nil
}

// WaitOwner waits for verified native cleanup after ReleaseOwner revoked the
// owner. It is used by the durable registry before it permits epoch reuse.
func (m *Manager) WaitOwner(ctx context.Context, value sandbox.Owner) error {
	m.mu.Lock()
	record := m.owners[key(value)]
	if record == nil {
		m.mu.Unlock()
		return sandbox.ErrClosed
	}
	if record.value.Epoch != value.Epoch {
		m.mu.Unlock()
		return sandbox.ErrStale
	}
	done := record.done
	m.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return record.cleanup
}
func (m *Manager) revokeLocked(record *owner) {
	if record.revoked {
		return
	}
	record.revoked = true
	record.cancel()
	delete(m.names, nameKey{record.value.Workspace, record.value.Name})
	go m.cleanup(record)
}
func (m *Manager) cleanup(record *owner) {
	record.operation <- struct{}{}
	defer func() { <-record.operation }()
	var result error
	for name, s := range record.slots {
		if err := s.instance.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("slot %s cleanup: %w", name, err))
			continue
		}
		delete(record.slots, name)
		m.mu.Lock()
		m.instances--
		m.mu.Unlock()
	}
	m.mu.Lock()
	record.cleanup = result
	close(record.done)
	m.mu.Unlock()
}
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		<-done
		return m.closeErr
	}
	m.closed = true
	records := make([]*owner, 0, len(m.owners))
	for _, record := range m.owners {
		records = append(records, record)
		m.revokeLocked(record)
	}
	m.mu.Unlock()
	var result error
	for _, record := range records {
		<-record.done
		result = errors.Join(result, record.cleanup)
	}
	m.mu.Lock()
	m.closeErr = result
	close(m.closeDone)
	m.mu.Unlock()
	return result
}

type bound struct {
	manager *Manager
	owner   *owner
}

func (b *bound) active() error {
	b.manager.mu.Lock()
	defer b.manager.mu.Unlock()
	if b.manager.closed || b.owner.revoked {
		return sandbox.ErrClosed
	}
	return nil
}
func (b *bound) Acquire(ctx context.Context, name string, template sandbox.TemplateID) (sandbox.Sandbox, error) {
	if sandbox.ValidateName(name) != nil || sandbox.ValidateName(string(template)) != nil {
		return nil, sandbox.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.active(); err != nil {
		return nil, err
	}
	if err := b.owner.lock(ctx); err != nil {
		return nil, err
	}
	defer b.owner.unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.active(); err != nil {
		return nil, err
	}
	if old := b.owner.slots[name]; old != nil {
		if old.template != template {
			return nil, sandbox.ErrConflict
		}
		select {
		case <-old.instance.Done():
			return nil, sandbox.ErrClosed
		default:
		}
		if old.instance.Status().State != sandbox.Ready {
			return nil, sandbox.ErrUnavailable
		}
		return &view{Sandbox: old.instance, bound: b, name: name, generation: old.generation}, nil
	}
	m := b.manager
	m.mu.Lock()
	if m.closed || b.owner.revoked {
		m.mu.Unlock()
		return nil, sandbox.ErrClosed
	}
	if m.instances >= m.limits.Instances || m.nextGeneration == ^uint64(0) {
		m.mu.Unlock()
		return nil, sandbox.ErrQuota
	}
	m.instances++
	m.nextGeneration++
	generation := m.nextGeneration
	value := b.owner.value
	m.mu.Unlock()
	// The instance owns this context beyond Prepare. Caller cancellation affects
	// preparation only; later caller cancellation does not destroy a ready slot.
	lifetime, cancel := context.WithCancel(b.owner.ctx)
	stop := context.AfterFunc(ctx, cancel)
	instance, err := m.backend.Prepare(lifetime, value, name, template, generation)
	stopped := stop()
	if (ctx.Err() != nil || !stopped) && err == nil {
		err = ctx.Err()
		if err == nil {
			err = context.Canceled
		}
	}
	if err == nil && instance == nil {
		err = sandbox.ErrLaunch
	}
	if err == nil {
		err = b.active()
	}
	if err == nil {
		status := instance.Status()
		if status.State != sandbox.Ready || status.Owner != value || status.Generation != generation || status.Template != template {
			err = sandbox.ErrLaunch
		}
		select {
		case <-instance.Done():
			err = sandbox.ErrClosed
		default:
		}
	}
	if err != nil {
		cancel()
		if instance != nil {
			if cleanupErr := instance.Close(); cleanupErr != nil {
				// Keep both the allocation and the failed instance. Cleanup failure must
				// not permit state reuse or a replacement launch.
				b.owner.slots[name] = &slot{template, instance, generation}
				m.mu.Lock()
				m.revokeLocked(b.owner)
				m.mu.Unlock()
				return nil, errors.Join(err, cleanupErr)
			}
		}
		m.mu.Lock()
		m.instances--
		m.mu.Unlock()
		return nil, err
	}
	// Keep the cancellation function with the slot, including successful Remove.
	held := &cancelSandbox{Sandbox: instance, cancel: cancel}
	m.mu.Lock()
	if m.closed || b.owner.revoked {
		m.mu.Unlock()
		cleanupErr := held.Close()
		if cleanupErr != nil {
			b.owner.slots[name] = &slot{template, held, generation}
		} else {
			m.mu.Lock()
			m.instances--
			m.mu.Unlock()
		}
		return nil, errors.Join(sandbox.ErrClosed, cleanupErr)
	}
	b.owner.slots[name] = &slot{template, held, generation}
	m.mu.Unlock()
	return &view{Sandbox: held, bound: b, name: name, generation: generation}, nil
}
func (b *bound) Remove(ctx context.Context, name string) error {
	if sandbox.ValidateName(name) != nil {
		return sandbox.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.owner.lock(ctx); err != nil {
		return err
	}
	defer b.owner.unlock()
	if err := b.active(); err != nil {
		return err
	}
	s := b.owner.slots[name]
	if s == nil {
		return nil
	}
	if err := s.instance.Close(); err != nil {
		b.manager.mu.Lock()
		b.manager.revokeLocked(b.owner)
		b.manager.mu.Unlock()
		return err
	}
	delete(b.owner.slots, name)
	b.manager.mu.Lock()
	b.manager.instances--
	b.manager.mu.Unlock()
	return nil
}

type cancelSandbox struct {
	sandbox.Sandbox
	cancel context.CancelFunc
}

func (s *cancelSandbox) Close() error { s.cancel(); return s.Sandbox.Close() }

// view supplies current name metadata without changing the live instance.
// The backend enforces revocation on all streams through its lifetime context.
type view struct {
	sandbox.Sandbox
	bound      *bound
	name       string
	generation uint64
}

func (v *view) Status() sandbox.Status {
	result := v.Sandbox.Status().Clone()
	v.bound.manager.mu.Lock()
	result.Owner = v.bound.owner.value
	revoked := v.bound.owner.revoked
	v.bound.manager.mu.Unlock()
	if revoked && result.State == sandbox.Ready {
		result.State = sandbox.Closing
	}
	return result
}

// Close removes only this generation. An old handle cannot close its replacement.
func (v *view) Close() error {
	b := v.bound
	if err := b.owner.lock(context.Background()); err != nil {
		return err
	}
	defer b.owner.unlock()
	if err := b.active(); err != nil {
		return err
	}
	s := b.owner.slots[v.name]
	if s == nil {
		return nil
	}
	if s.generation != v.generation {
		return sandbox.ErrStale
	}
	if err := s.instance.Close(); err != nil {
		b.manager.mu.Lock()
		b.manager.revokeLocked(b.owner)
		b.manager.mu.Unlock()
		return err
	}
	delete(b.owner.slots, v.name)
	b.manager.mu.Lock()
	b.manager.instances--
	b.manager.mu.Unlock()
	return nil
}

func (o *owner) lock(ctx context.Context) error {
	select {
	case o.operation <- struct{}{}:
		if err := ctx.Err(); err != nil {
			o.unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-o.ctx.Done():
		return sandbox.ErrClosed
	}
}
func (o *owner) unlock() { <-o.operation }
