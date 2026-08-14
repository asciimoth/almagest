package almagest

import "github.com/asciimoth/almagest/lib/storage"

var (
	ErrKVClosed       = storage.ErrKVClosed
	ErrKVInvalidName  = storage.ErrKVInvalidName
	ErrKVInvalidValue = storage.ErrKVInvalidValue
	ErrKVKindConflict = storage.ErrKVKindConflict
	ErrKVRoot         = storage.ErrKVRoot
)

type KVManager = storage.KVManager
type KValue = storage.KValue
type KVReader = storage.KVReader
type KVReadTx = storage.KVReadTx
type KVWriteTx = storage.KVWriteTx
type KVStore = storage.KVStore
