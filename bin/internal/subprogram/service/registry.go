package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	sandbox "github.com/asciimoth/gobox"
)

const registryRevision = 1

type ownerRecord struct {
	Owner       sandbox.Owner `json:"owner"`
	Active      bool          `json:"active"`
	Orphan      bool          `json:"orphan"`
	Quarantined bool          `json:"quarantined,omitempty"`
}

type renameReceipt struct {
	Request sandbox.OwnerRename `json:"request"`
	Result  sandbox.Owner       `json:"result"`
}

type registryState struct {
	Revision int                      `json:"revision"`
	Owners   map[string]ownerRecord   `json:"owners"`
	Renames  map[string]renameReceipt `json:"renames"`
}

type registry struct {
	mu           sync.Mutex
	root         string
	path         string
	state        registryState
	receiptLimit int
	save         func() error
	lock         *os.File
}

func openRegistry(root string, receiptLimit int) (*registry, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, sandbox.ErrInvalid
	}
	r := &registry{root: root, path: filepath.Join(root, "owners-v1.json"), receiptLimit: receiptLimit, state: registryState{Revision: registryRevision, Owners: map[string]ownerRecord{}, Renames: map[string]renameReceipt{}}}
	r.save = r.saveFileLocked
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rootHandle.Close() }()
	lock, err := rootHandle.OpenFile("registry.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	lockInfo, statErr := lock.Stat()
	if statErr != nil || !lockInfo.Mode().IsRegular() || registryFileHasAliases(lock, lockInfo) {
		_ = lock.Close()
		return nil, errors.Join(sandbox.ErrDenied, statErr)
	}
	if err = lockRegistryFile(lock); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("%w: sandbox registry is in use", sandbox.ErrUnavailable)
	}
	r.lock = lock
	keepLock := false
	defer func() {
		if !keepLock {
			_ = r.closeLock()
		}
	}()
	info, err = rootHandle.Lstat("owners-v1.json")
	if errors.Is(err, fs.ErrNotExist) {
		keepLock = true
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, sandbox.ErrDenied
	}
	file, err := rootHandle.Open("owners-v1.json")
	if err != nil {
		return nil, err
	}
	heldInfo, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, heldInfo) || registryFileHasAliases(file, heldInfo) {
		_ = file.Close()
		return nil, errors.Join(sandbox.ErrDenied, statErr)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(data) > 16<<20 {
		return nil, sandbox.ErrQuota
	}
	if err = json.Unmarshal(data, &r.state); err != nil {
		return nil, fmt.Errorf("decode owner registry: %w", err)
	}
	if err = r.validateState(); err != nil {
		return nil, sandbox.ErrInvalid
	}
	// The exclusive registry lock proves that no live service owns these active
	// records. Fence each affected owner instead of disabling unrelated owners.
	recovered := false
	for key, record := range r.state.Owners {
		if record.Active || record.Quarantined {
			record.Active = false
			record.Orphan = true
			record.Quarantined = true
			r.state.Owners[key] = record
			recovered = true
		}
	}
	if recovered {
		if err = r.save(); err != nil {
			return nil, fmt.Errorf("record sandbox recovery quarantine: %w", err)
		}
	}
	keepLock = true
	return r, nil
}

func (r *registry) validateState() error {
	if r.state.Revision != registryRevision || r.state.Owners == nil || r.state.Renames == nil || len(r.state.Renames) > r.receiptLimit {
		return sandbox.ErrInvalid
	}
	names := map[string]bool{}
	for key, record := range r.state.Owners {
		if key != record.Owner.Key || !validRegistryOwner(record.Owner) || record.Active == record.Orphan || record.Quarantined && (record.Active || !record.Orphan) {
			return sandbox.ErrInvalid
		}
		if record.Active {
			name := record.Owner.Workspace + "\x00" + record.Owner.Name
			if names[name] {
				return sandbox.ErrInvalid
			}
			names[name] = true
		}
	}
	for operationID, receipt := range r.state.Renames {
		request, result := receipt.Request, receipt.Result
		owner, ok := r.state.Owners[result.Key]
		if operationID != request.OperationID || sandbox.ValidateName(operationID) != nil || !validRegistryOwner(request.Owner) ||
			!validRegistryOwner(result) || !ok || request.NewName != result.Name || request.Owner.Key != result.Key ||
			request.Owner.Workspace != result.Workspace || request.Owner.Class != result.Class || request.Owner.Epoch != result.Epoch ||
			request.Owner.NameRevision == ^uint64(0) || request.Owner.NameRevision+1 != result.NameRevision || owner.Owner.NameRevision < result.NameRevision {
			return sandbox.ErrInvalid
		}
	}
	return nil
}

func (r *registry) issue(proposed sandbox.Owner) (sandbox.Owner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if proposed.Workspace == "" || proposed.Class == "" || proposed.Name == "" {
		return sandbox.Owner{}, sandbox.ErrInvalid
	}
	if proposed.Key != "" {
		record, ok := r.state.Owners[proposed.Key]
		if !ok {
			return sandbox.Owner{}, sandbox.ErrDenied
		}
		if record.Owner.Workspace != proposed.Workspace || record.Owner.Class != proposed.Class {
			return sandbox.Owner{}, sandbox.ErrDenied
		}
		if record.Quarantined {
			return sandbox.Owner{}, sandbox.ErrUnavailable
		}
		if record.Active {
			if record.Owner != proposed {
				return sandbox.Owner{}, sandbox.ErrStale
			}
			return record.Owner, nil
		}
		if record.Owner.Name != proposed.Name {
			return sandbox.Owner{}, sandbox.ErrDenied
		}
		if record.Owner.Epoch == ^uint64(0) {
			return sandbox.Owner{}, sandbox.ErrQuota
		}
		previous := record
		record.Owner.Epoch++
		record.Owner.NameRevision++
		record.Active = true
		record.Orphan = false
		r.state.Owners[proposed.Key] = record
		if err := r.save(); err != nil {
			r.state.Owners[proposed.Key] = previous
			return sandbox.Owner{}, err
		}
		return record.Owner, nil
	}
	for _, record := range r.state.Owners {
		if (record.Active || record.Quarantined) && record.Owner.Workspace == proposed.Workspace && record.Owner.Name == proposed.Name {
			return sandbox.Owner{}, sandbox.ErrConflict
		}
	}
	for attempts := 0; attempts < 16; attempts++ {
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			return sandbox.Owner{}, err
		}
		proposed.Key = hex.EncodeToString(random[:])
		if _, exists := r.state.Owners[proposed.Key]; exists {
			continue
		}
		proposed.Epoch = 1
		proposed.NameRevision = 1
		r.state.Owners[proposed.Key] = ownerRecord{Owner: proposed, Active: true}
		if err := r.save(); err != nil {
			delete(r.state.Owners, proposed.Key)
			return sandbox.Owner{}, err
		}
		return proposed, nil
	}
	return sandbox.Owner{}, sandbox.ErrUnavailable
}

func (r *registry) Authorize(_ context.Context, owner sandbox.Owner) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.state.Owners[owner.Key]
	if !ok {
		return sandbox.ErrDenied
	}
	if !record.Active {
		if record.Quarantined {
			return sandbox.ErrUnavailable
		}
		return sandbox.ErrClosed
	}
	if record.Owner != owner {
		return sandbox.ErrStale
	}
	return nil
}

func (r *registry) counts() (active, orphaned, quarantined int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range r.state.Owners {
		if record.Active {
			active++
		}
		if record.Orphan {
			orphaned++
		}
		if record.Quarantined {
			quarantined++
		}
	}
	return active, orphaned, quarantined
}

func (r *registry) rename(request sandbox.OwnerRename) (sandbox.Owner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !validRegistryOwner(request.Owner) || !validRegistryLabel(request.NewName) || sandbox.ValidateName(request.OperationID) != nil {
		return sandbox.Owner{}, sandbox.ErrInvalid
	}
	if receipt, ok := r.state.Renames[request.OperationID]; ok {
		if receipt.Request != request {
			return sandbox.Owner{}, sandbox.ErrConflict
		}
		return receipt.Result, nil
	}
	record, ok := r.state.Owners[request.Owner.Key]
	if !ok || !record.Active {
		return sandbox.Owner{}, sandbox.ErrClosed
	}
	if record.Owner != request.Owner {
		return sandbox.Owner{}, sandbox.ErrStale
	}
	for key, other := range r.state.Owners {
		if key != request.Owner.Key && other.Active && other.Owner.Workspace == request.Owner.Workspace && other.Owner.Name == request.NewName {
			return sandbox.Owner{}, sandbox.ErrConflict
		}
	}
	if len(r.state.Renames) >= r.receiptLimit {
		return sandbox.Owner{}, sandbox.ErrQuota
	}
	previous := record
	record.Owner.Name = request.NewName
	record.Owner.NameRevision++
	r.state.Owners[request.Owner.Key] = record
	r.state.Renames[request.OperationID] = renameReceipt{Request: request, Result: record.Owner}
	if err := r.save(); err != nil {
		r.state.Owners[request.Owner.Key] = previous
		delete(r.state.Renames, request.OperationID)
		return sandbox.Owner{}, err
	}
	return record.Owner, nil
}

func (r *registry) rollbackRename(operationID string, expected sandbox.Owner) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	receipt, ok := r.state.Renames[operationID]
	if !ok || receipt.Result != expected {
		return sandbox.ErrStale
	}
	record := r.state.Owners[expected.Key]
	previous := record
	previousReceipt := receipt
	record.Owner = receipt.Request.Owner
	r.state.Owners[expected.Key] = record
	delete(r.state.Renames, operationID)
	if err := r.save(); err != nil {
		r.state.Owners[expected.Key] = previous
		r.state.Renames[operationID] = previousReceipt
		return err
	}
	return nil
}

func (r *registry) release(owner sandbox.Owner) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.state.Owners[owner.Key]
	if !ok {
		return sandbox.ErrClosed
	}
	if record.Owner.Epoch != owner.Epoch {
		return sandbox.ErrStale
	}
	previous := record
	record.Active = false
	record.Orphan = true
	r.state.Owners[owner.Key] = record
	if err := r.save(); err != nil {
		r.state.Owners[owner.Key] = previous
		return err
	}
	return nil
}

func (r *registry) deactivateAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := make(map[string]ownerRecord, len(r.state.Owners))
	for key, record := range r.state.Owners {
		previous[key] = record
	}
	for key, record := range r.state.Owners {
		if record.Active {
			record.Active = false
			record.Orphan = true
			r.state.Owners[key] = record
		}
	}
	if err := r.save(); err != nil {
		r.state.Owners = previous
		return err
	}
	return nil
}

func (r *registry) closeLock() error {
	if r.lock == nil {
		return nil
	}
	file := r.lock
	r.lock = nil
	return errors.Join(unlockRegistryFile(file), file.Close())
}

func (r *registry) saveFileLocked() error {
	data, err := json.Marshal(r.state)
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return sandbox.ErrQuota
	}
	temporary, err := os.CreateTemp(r.root, ".owners-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(name)
		}
	}()
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if info, statErr := os.Lstat(r.path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return sandbox.ErrDenied
	}
	if err = replaceRegistryFile(name, r.path); err != nil {
		return err
	}
	cleanup = false
	return syncRegistryDirectory(r.root)
}

func validRegistryOwner(owner sandbox.Owner) bool {
	return sandbox.ValidateName(owner.Workspace) == nil && sandbox.ValidateName(owner.Key) == nil &&
		validRegistryLabel(owner.Class) && validRegistryLabel(owner.Name) && owner.Epoch > 0 && owner.NameRevision > 0
}

func validRegistryLabel(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if char < 32 || char == 127 {
			return false
		}
	}
	return true
}
