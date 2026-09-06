package storage

import (
	"context"
	"errors"
)

var (
	ErrKVClosed       = errors.New("kv: database is closed")
	ErrKVInvalidName  = errors.New("kv: invalid key or subtree name")
	ErrKVInvalidValue = errors.New("kv: invalid value")
	ErrKVKindConflict = errors.New("kv: name is used by a different entry kind")
	ErrKVRoot         = errors.New("kv: cannot drop the root subtree")
	ErrKVInvalidOwner = errors.New("kv: invalid owner node name")
	ErrKVOwnerDeleted = errors.New("kv: owner node was deleted")
)

// KVManager opens logical key-value stores.
type KVManager interface {
	DefaultStore() KVStore
	PluginStore(name string) KVStore
}

// KValue is one stored key-value entry.
type KValue struct {
	Data []byte

	// Hidden reports that user interfaces should hide the value by default.
	Hidden bool
	// String reports that Data is text, not opaque bytes.
	String bool
	// Owner is the current owner node name. It is empty for an unowned value.
	// Set ignores this read-only field; use KVStore.OwnedBy to claim entries.
	Owner string
}

// Clone returns a deep copy of v.
func (v KValue) Clone() KValue {
	v.Data = append([]byte(nil), v.Data...)
	return v
}

// KVReader operates on the current KV subtree.
//
// Keys and subtree names share one namespace. A name cannot simultaneously
// identify both a value and a child subtree.
type KVReader interface {
	// Exists reports whether this subtree is physically present.
	//
	// The abstract root always exists. A Subtree call may point to a subtree
	// that has not been created yet.
	Exists() bool

	// Owner returns the node name that owns the current subtree. It returns an
	// empty string when the subtree is unowned or does not exist.
	Owner() (string, error)

	// Get returns a reader-owned copy of the value. Clone it for use outside of
	// the transaction.
	//
	// found is false when the key does not exist. An empty stored value is
	// returned with found=true.
	Get(key string) (value *KValue, found bool, err error)

	// ForEachValue visits immediate values in the current subtree.
	//
	// It returns hidden values too. Iteration order is unspecified.
	ForEachValue(fn func(key string, value KValue) error) error

	// ForEachSubtree visits names of immediate child subtrees.
	//
	// Iteration order is unspecified.
	ForEachSubtree(fn func(name string) error) error
}

// KVReadTx is valid only during its View callback.
type KVReadTx interface {
	KVReader

	// Subtree returns another read transaction handle rooted below the current
	// subtree. It performs no I/O and does not create anything.
	Subtree(path ...string) KVReadTx
}

// KVWriteTx is valid only during its Update callback.
type KVWriteTx interface {
	KVReader

	// Subtree returns another writable transaction handle rooted below the
	// current subtree. It performs no I/O by itself.
	Subtree(path ...string) KVWriteTx

	// Ensure creates the current subtree and missing ancestors.
	// It is idempotent.
	Ensure() error

	// Set creates missing ancestors and stores the value.
	//
	// The implementation must copy value.Data before Set returns.
	Set(key string, value KValue) error

	// Delete removes a value. Deleting a missing value is successful.
	//
	// It must not delete a child subtree with the same name.
	Delete(key string) error

	// Drop recursively deletes the current subtree.
	//
	// Dropping a missing subtree is successful. Dropping the abstract root
	// returns ErrKVRoot.
	Drop() error
}

// KVStore is a long-lived, concurrency-safe handle to a logical subtree.
type KVStore interface {
	// Subtree returns a narrowly scoped Store.
	//
	// The returned Store can access only this path and its descendants.
	Subtree(path ...string) KVStore

	// OwnedBy returns a store whose successful Ensure and Set operations claim
	// the affected subtrees and values for owner. Missing ancestors at or below
	// this store's path are also claimed when an operation creates them.
	//
	// Ownership uses the current workspace node name. A storage implementation
	// can update existing handles when that node is renamed and revoke them when
	// it is deleted. Writes through a revoked handle return ErrKVOwnerDeleted.
	// Calling OwnedBy with an invalid node name returns a handle whose writes
	// fail with ErrKVInvalidOwner.
	//
	// Ownership controls lifecycle cleanup; it does not restrict access. An
	// owned subtree is deleted recursively even if a descendant has a different
	// owner.
	OwnedBy(owner string) KVStore

	// View provides a consistent read-only snapshot.
	View(ctx context.Context, fn func(KVReadTx) error) error

	// Update executes fn atomically.
	//
	// Returning nil commits. Returning an error rolls back and returns that
	// error to the caller.
	Update(ctx context.Context, fn func(KVWriteTx) error) error
}
