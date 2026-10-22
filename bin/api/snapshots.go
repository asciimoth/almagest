package api

import "github.com/asciimoth/almagest/bin/internal/snapshotlimits"

const (
	// SnapshotArchiveMaxBytes is the largest compressed snapshot archive that
	// can cross the JSON admin API. The limit bounds the byte slice and base64
	// copies that the current request protocol must keep in memory.
	SnapshotArchiveMaxBytes int64 = snapshotlimits.ArchiveBytes

	// SnapshotAPIMessageMaxBytes allows one maximum-size base64 archive plus
	// its JSON envelope and metadata. Admin transports use this value as their
	// per-message read limit.
	SnapshotAPIMessageMaxBytes int64 = snapshotlimits.APIMessageBytes
)

// SnapshotsMethod identifies a daemon file-storage snapshot operation.
type SnapshotsMethod string

const (
	MethodSnapshotsCreate  SnapshotsMethod = "create"
	MethodSnapshotsDelete  SnapshotsMethod = "delete"
	MethodSnapshotsExport  SnapshotsMethod = "export"
	MethodSnapshotsImport  SnapshotsMethod = "import"
	MethodSnapshotsList    SnapshotsMethod = "list"
	MethodSnapshotsRename  SnapshotsMethod = "rename"
	MethodSnapshotsRestore SnapshotsMethod = "restore"
)

// SnapshotsMethods returns all methods implemented by the snapshots surface.
func SnapshotsMethods() []string {
	return []string{
		string(MethodSnapshotsCreate),
		string(MethodSnapshotsDelete),
		string(MethodSnapshotsExport),
		string(MethodSnapshotsImport),
		string(MethodSnapshotsList),
		string(MethodSnapshotsRename),
		string(MethodSnapshotsRestore),
	}
}

// IsSnapshotsMutation reports whether a snapshots method changes daemon
// state. Creating, importing, deleting, and renaming snapshots write file
// storage. Restoring also replaces the current runtime state.
func IsSnapshotsMutation(method string) bool {
	switch SnapshotsMethod(method) {
	case MethodSnapshotsCreate, MethodSnapshotsDelete, MethodSnapshotsImport, MethodSnapshotsRename, MethodSnapshotsRestore:
		return true
	default:
		return false
	}
}

// SnapshotInfo describes one regular snapshot or one packaged example.
// Regular snapshots have UUID and Timestamp. Examples have Example, Name, and
// Comment. Timestamp is Unix time in nanoseconds.
type SnapshotInfo struct {
	UUID      string `json:"uuid,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
	Name      string `json:"name,omitempty"`
	Comment   string `json:"comment,omitempty"`
	Example   string `json:"example,omitempty"`
}

// SnapshotsCreateRequest creates a snapshot with an optional display name.
// Names do not identify snapshots and do not need to be unique.
type SnapshotsCreateRequest struct {
	Name string `json:"name,omitempty"`
}

// SnapshotsImportRequest adds one exported archive to the snapshot store.
// Name overrides the name in the archive when it is present. A non-nil empty
// string removes the archived name.
type SnapshotsImportRequest struct {
	Archive []byte  `json:"archive"`
	Name    *string `json:"name,omitempty"`
}

// SnapshotsExportResponse contains one snapshot archive and its metadata.
// Archive uses standard base64 encoding when this value is encoded as JSON.
type SnapshotsExportResponse struct {
	Snapshot SnapshotInfo `json:"snapshot"`
	Archive  []byte       `json:"archive"`
}

// SnapshotsRenameRequest changes the display name of one snapshot. An empty
// name removes the current display name.
type SnapshotsRenameRequest struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// SnapshotsListResponse contains regular snapshots in newest-first order,
// followed by packaged examples in identifier order.
type SnapshotsListResponse struct {
	Snapshots []SnapshotInfo `json:"snapshots"`
}

// SnapshotUUIDRequest identifies a snapshot by its unique UUID.
type SnapshotUUIDRequest struct {
	UUID string `json:"uuid"`
}

// SnapshotsRestoreRequest selects one daemon-owned snapshot or one packaged
// example. Exactly one field must be present.
type SnapshotsRestoreRequest struct {
	UUID    string `json:"uuid,omitempty"`
	Example string `json:"example,omitempty"`
}

// SnapshotsRestoreResponse reports the restored snapshot and whether the
// daemon also persisted its regular config. A config write error is non-fatal:
// restored config remains active in memory.
type SnapshotsRestoreResponse struct {
	Snapshot         SnapshotInfo `json:"snapshot"`
	ConfigWritten    bool         `json:"config_written"`
	ConfigWriteError string       `json:"config_write_error,omitempty"`
}
